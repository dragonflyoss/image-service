# Nydus: Dragonfly Container Image Service

The nydus project implements a user space filesystem on top of
a container image format that improves over the current OCI image
specification. Its key features include:

* Container images are downloaded on demand
* Chunk level data deduplication
* Flatten image metadata and data to remove all intermediate layers
* Only usable image data is saved when building a container image
* Only usable image data is downloaded when running a container
* End-to-end image data integrity
* Compatible with the OCI artifacts spec and distribution spec
* Integrated with existing CNCF project Dragonfly to support image distribution in large clusters
* Different container image storage backends are supported

Currently the repository includes following tools:

* A `nydusify` tool to convert an OCI format container image into a nydus format container image
* A `containerd-nydus-grpc` daemon to serve as containerd remote snapshotter and setup container rootfs with nydus
* A `nydus-image` tool to convert an unpacked container image into a nydus format image
* A `nydusd` daemon to parse a nydus format image and expose a FUSE mountpoint for containers to access

## Build Binary

``` shell
# build debug binary
make
# build release binary
make release
# build static binary with docker
make docker-static
```

## Build Nydus Image

Build Nydus image from directory source: [Nydus Image Builder](./docs/nydus-image.md).

Convert OCI image to Nydus image: [Nydusify](./docs/nydusify.md).

## Build Nydus Snapshotter

Build and run Nydus snapshotter: [Nydus Snapshotter](./contrib/nydus-snapshotter/README.md)

## Run Nydusd Daemon

Run Nydusd Daemon to serve Nydus image: [Nydusd](./docs/nydusd.md).

## Learn Concepts and Commands

Browse the documentation to learn more. Here are some topics you may be interested in:

- [A Nydus Tutorial for Beginners](./docs/tutorial.md)
- Our talk on Open Infra Summit 2020: [Toward Next Generation Container Image](https://drive.google.com/file/d/1LRfLUkNxShxxWU7SKjc_50U0N9ZnGIdV/view)
- [Nydus Design Doc](./docs/nydus-design.md)
