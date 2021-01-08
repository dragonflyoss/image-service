// Copyright 2020 Ant Group. All rights reserved.
//
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"

	"contrib/nydusify/utils"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/docker/docker/pkg/archive"
	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const currentRafsVersion = 0x500

type Opt struct {
	MaxRecords     uint
	Ref            string
	Insecure       bool
	DockerV2Format bool
}

type Cache struct {
	remote        *Remote
	opt           Opt
	pulledRecords map[digest.Digest]int
	pushedRecords []CacheRecordWithChainID
	ctx           context.Context
}

// New creates nydus build cache instance, nydus build cache creates
// an image to store cache records in its image manifest, every record
// presents the relationship like:
//
// source_layer_chainid -> (nydus_blob_layer_digest, nydus_bootstrap_layer_digest)
// If the converter hit cache record during build source layer, we can
// skip the layer building.
//
// Here is the build cache workflow:
// 1. Import cache records from registry;
// 2. Check cache record using source layer ChainID before layer build,
//    skip layer build if the cache hit;
// 3. Export new cache records to registry;
func New(opt Opt) (*Cache, error) {
	remote, err := NewRemote(RemoteOpt{
		Ref:      opt.Ref,
		Insecure: opt.Insecure,
	})
	if err != nil {
		return nil, errors.Wrap(err, "init remote")
	}

	cache := &Cache{
		remote:        remote,
		opt:           opt,
		pulledRecords: make(map[digest.Digest]int),
		pushedRecords: []CacheRecordWithChainID{},
		ctx:           context.Background(),
	}

	return cache, nil
}

func (cache *Cache) exportRecordsToLayers() []ocispec.Descriptor {
	layers := []ocispec.Descriptor{}

	for _, record := range cache.pushedRecords {
		desc := *record.NydusBootstrapDesc
		desc.Platform = nil
		desc.URLs = nil
		desc.Annotations = make(map[string]string)
		if cache.opt.DockerV2Format {
			desc.MediaType = images.MediaTypeDockerSchema2LayerGzip
		} else {
			desc.MediaType = ocispec.MediaTypeImageLayerGzip
		}
		desc.Annotations[utils.LayerAnnotationNydusBootstrap] = "true"
		desc.Annotations[utils.LayerAnnotationNydusSourceChainID] = record.SourceChainID.String()
		desc.Annotations[utils.LayerAnnotationNydusRafsVersion] = strconv.FormatInt(currentRafsVersion, 16)
		desc.Annotations[utils.LayerAnnotationUncompressed] = record.NydusBootstrapDiffID.String()
		if record.NydusBlobDesc != nil {
			desc.Annotations[utils.LayerAnnotationNydusBlobDigest] = record.NydusBlobDesc.Digest.String()
			desc.Annotations[utils.LayerAnnotationNydusBlobSize] = strconv.FormatInt(record.NydusBlobDesc.Size, 10)
		}
		layers = append(layers, desc)
	}

	return layers
}

func (cache *Cache) importLayersToRecords(layers []ocispec.Descriptor) {
	pulledRecords := make(map[digest.Digest]int)
	pushedRecords := []CacheRecordWithChainID{}

	for idx, layer := range layers {
		var nydusBlobDesc *ocispec.Descriptor
		if layer.Annotations == nil {
			continue
		}
		nydusBlobDigestStr, ok1 := layer.Annotations[utils.LayerAnnotationNydusBlobDigest]
		nydusBlobSize, ok2 := layer.Annotations[utils.LayerAnnotationNydusBlobSize]
		nydusBlobDigest := digest.Digest(nydusBlobDigestStr)
		if ok1 && ok2 && nydusBlobDigest.Validate() == nil {
			size, err := strconv.ParseInt(nydusBlobSize, 10, 64)
			if err == nil {
				nydusBlobDesc = &ocispec.Descriptor{
					MediaType: utils.MediaTypeNydusBlob,
					Digest:    nydusBlobDigest,
					Size:      size,
				}
			}
		}
		sourceChainIDStr, ok1 := layer.Annotations[utils.LayerAnnotationNydusSourceChainID]
		nydusRafsVersionStr, ok2 := layer.Annotations[utils.LayerAnnotationNydusRafsVersion]
		bootstrapDiffIDStr, ok3 := layer.Annotations[utils.LayerAnnotationUncompressed]
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		nydusRafsVersion, err := strconv.ParseInt(nydusRafsVersionStr, 16, 64)
		if err != nil || nydusRafsVersion != currentRafsVersion {
			continue
		}
		sourceChainID := digest.Digest(sourceChainIDStr)
		if sourceChainID.Validate() != nil {
			continue
		}
		bootstrapDiffID := digest.Digest(bootstrapDiffIDStr)
		if bootstrapDiffID.Validate() != nil {
			continue
		}
		cacheRecord := CacheRecordWithChainID{
			SourceChainID: sourceChainID,
			CacheRecord: CacheRecord{
				NydusBlobDesc:        nydusBlobDesc,
				NydusBootstrapDesc:   &layers[idx],
				NydusBootstrapDiffID: bootstrapDiffID,
			},
		}
		pulledRecords[sourceChainID] = idx
		pushedRecords = append(pushedRecords, cacheRecord)
	}

	cache.pulledRecords = pulledRecords
	cache.pushedRecords = pushedRecords
}

// Export pushes cache manifest index to remote registry
func (cache *Cache) Export() error {
	if len(cache.pushedRecords) == 0 {
		return nil
	}

	layers := cache.exportRecordsToLayers()

	// Push cache manifest to remote registry
	mediaType := ocispec.MediaTypeImageManifest
	if cache.opt.DockerV2Format {
		mediaType = images.MediaTypeDockerSchema2Manifest
	}

	manifest := CacheManifest{
		MediaType: mediaType,
		Manifest: ocispec.Manifest{
			Versioned: specs.Versioned{
				SchemaVersion: 2,
			},
			// Just for registry API compatibility, registry requires a
			// valid config field with existed blob.
			Config: ocispec.Descriptor{
				Digest:    layers[0].Digest,
				Size:      layers[0].Size,
				MediaType: layers[0].MediaType,
			},
			Layers: layers,
			Annotations: map[string]string{
				utils.ManifestNydusCache: utils.ManifestNydusCacheV1,
			},
		},
	}

	manifestDesc, manifestBytes, err := utils.MarshalToDesc(manifest, manifest.MediaType)
	if err != nil {
		return err
	}

	manifestWriter, err := cache.remote.Push(cache.ctx, *manifestDesc, false)
	if err != nil {
		return errors.Wrap(err, "push cache manifest")
	}
	if manifestWriter != nil {
		defer manifestWriter.Close()
		if err := content.Copy(
			cache.ctx, manifestWriter, bytes.NewReader(manifestBytes), manifestDesc.Size, manifestDesc.Digest,
		); err != nil {
			return errors.Wrap(err, "write cache manifest")
		}
	}

	return nil
}

// Import pulls cache manifest index from remote registry
func (cache *Cache) Import() error {
	configDesc, err := cache.remote.Resolve(cache.ctx)
	if err != nil {
		return errors.Wrap(err, "resolve cache image")
	}

	// Fetch cache config from remote registry
	configReader, err := cache.remote.Pull(cache.ctx, *configDesc, true)
	if err != nil {
		return errors.Wrap(err, "pull cache image")
	}
	defer configReader.Close()

	configBytes, err := ioutil.ReadAll(configReader)
	if err != nil {
		return errors.Wrap(err, "read cache manifest")
	}

	var config CacheManifest
	if err := json.Unmarshal(configBytes, &config); err != nil {
		return err
	}

	cache.importLayersToRecords(config.Layers)

	return nil
}

func (cache *Cache) Check(layerChainID digest.Digest) (*CacheRecordWithChainID, error) {
	idx, ok := cache.pulledRecords[layerChainID]
	if !ok {
		return nil, nil
	}
	if idx+1 > len(cache.pushedRecords) {
		return nil, nil
	}
	found := cache.pushedRecords[idx]

	// Check bootstrap layer on remote
	reader, err := cache.remote.Pull(cache.ctx, *found.NydusBootstrapDesc, true)
	if err != nil {
		return nil, errors.Wrap(err, "check bootstrap layer")
	}
	defer reader.Close()

	// Check blob layer on remote
	if found.NydusBlobDesc != nil {
		reader, err := cache.remote.Pull(cache.ctx, *found.NydusBlobDesc, true)
		if err != nil {
			return nil, errors.Wrap(err, "check blob layer")
		}
		defer reader.Close()
	}

	return &found, nil
}

func (cache *Cache) Push(records []CacheRecordWithChainID) {
	moveFront := map[digest.Digest]bool{}
	for _, record := range records {
		moveFront[record.SourceChainID] = true
	}

	pushedRecords := records
	for _, record := range cache.pushedRecords {
		if !moveFront[record.SourceChainID] {
			pushedRecords = append(pushedRecords, record)
			if len(pushedRecords) >= int(cache.opt.MaxRecords) {
				break
			}
		}
	}

	if len(pushedRecords) > int(cache.opt.MaxRecords) {
		cache.pushedRecords = pushedRecords[:int(cache.opt.MaxRecords)]
	} else {
		cache.pushedRecords = pushedRecords
	}
}

func (cache *Cache) PullBootstrap(bootstrapDesc *ocispec.Descriptor, target string) error {
	reader, err := cache.remote.Pull(cache.ctx, *bootstrapDesc, true)
	if err != nil {
		return errors.Wrap(err, "pull cached bootstrap layer")
	}
	defer reader.Close()

	rdr, err := archive.DecompressStream(reader)
	if err != nil {
		return errors.Wrap(err, "decompress cached bootstrap layer")
	}

	found := false
	tr := tar.NewReader(rdr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			} else {
				return err
			}
		}
		if hdr.Name == utils.BootstrapFileNameInLayer {
			file, err := os.Create(target)
			if err != nil {
				return err
			}
			defer file.Close()
			if _, err := io.Copy(file, tr); err != nil {
				return err
			}
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("Invalid bootstrap layer in cache")
	}

	return nil
}

func (cache *Cache) PushBootstrap(reader io.Reader, bootstrapDesc *ocispec.Descriptor) error {
	return cache.remote.PushByReader(cache.ctx, bootstrapDesc, true, reader)
}

func (cache *Cache) GetRef() string {
	return cache.opt.Ref
}