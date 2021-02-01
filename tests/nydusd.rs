// Copyright 2020 Ant Group. All rights reserved.
//
// SPDX-License-Identifier: Apache-2.0

use std::fs::{self, File};
use std::io::{Read, Result, Write};
use std::path::PathBuf;
use std::thread::*;
use std::time;

use nydus_utils::{einval, eother, exec};
use rafs::metadata::RafsMode;

const NYDUSD: &str = "./target-fusedev/debug/nydusd";

pub struct Nydusd {
    work_dir: PathBuf,
    mount_path: PathBuf,
    bootstrap_file_name: PathBuf,
    pub api_sock: PathBuf,
}

pub fn new(
    work_dir: &PathBuf,
    enable_cache: bool,
    cache_compressed: bool,
    rafs_mode: RafsMode,
    bootstrap_file_name: PathBuf,
    api_sock: PathBuf,
    digest_validate: bool,
) -> Result<Nydusd> {
    let mount_path = work_dir.join("mnt");
    fs::create_dir_all(mount_path.clone())?;

    let cache_path = work_dir.join("cache");
    fs::create_dir_all(cache_path.clone())?;

    let cache = format!(
        r###"
        ,"cache": {{
            "type": "blobcache",
            "config": {{
                "compressed": {},
                "work_dir": {:?}
            }}
        }}
    "###,
        cache_compressed,
        work_dir.join("cache")
    );

    let config = format!(
        r###"
        {{
            "device": {{
                "backend": {{
                    "type": "localfs",
                    "config": {{
                        "dir": {:?},
                        "readahead": true
                    }}
                }}
                {}
            }},
            "mode": "{}",
            "digest_validate": {},
            "iostats_files": true
        }}
        "###,
        work_dir.join("blobs"),
        if enable_cache { cache } else { String::new() },
        rafs_mode,
        digest_validate,
    );

    File::create(work_dir.join("config.json"))?.write_all(config.as_bytes())?;

    Ok(Nydusd {
        work_dir: work_dir.clone(),
        mount_path,
        bootstrap_file_name,
        api_sock,
    })
}

impl Nydusd {
    fn _start(&self, upgrade: bool) -> Result<()> {
        let work_dir = self.work_dir.clone();
        let mount_path = self.mount_path.clone();
        let bootstrap_file_name = self.bootstrap_file_name.clone();
        let api_sock = self.api_sock.clone();

        let upgrade_arg = if upgrade { "--upgrade" } else { "" };

        spawn(move || {
            exec(
                format!(
                    "{} {} --config {:?} --apisock {:?} --mountpoint {:?} --bootstrap {:?} --log-level info --id {:?} --supervisor {:?}",
                    NYDUSD,
                    upgrade_arg,
                    work_dir.join("config.json"),
                    work_dir.join(api_sock),
                    mount_path,
                    work_dir.join(bootstrap_file_name),
                    work_dir.file_name().unwrap(),
                    work_dir.join("supervisor.sock"),
                )
                .as_str(),
                false
            ).unwrap();
        });

        sleep(time::Duration::from_secs(2));

        if !upgrade && !self.is_mounted()? {
            return Err(eother!("nydusd mount failed"));
        }

        Ok(())
    }

    pub fn start(&self) -> Result<()> {
        self._start(false)
    }

    pub fn start_with_upgrade(&self) -> Result<()> {
        self._start(true)
    }

    pub fn check(&self, expect_texture: &str) -> Result<()> {
        let mount_path = self.work_dir.join("mnt");

        let tree_ret = exec(format!("tree -a -J -v {:?}", mount_path).as_str(), true)?;
        let md5_ret = exec(
            format!("find {:?} -type f -exec md5sum {{}} + | sort", mount_path).as_str(),
            true,
        )?;

        let ret = format!(
            "{}{}",
            tree_ret.replace(mount_path.to_str().unwrap(), ""),
            md5_ret.replace(mount_path.to_str().unwrap(), "")
        );

        let texture_file = format!("./tests/texture/{}", expect_texture);
        let mut texture = File::open(texture_file.clone())
            .map_err(|_| einval!(format!("invalid texture file path: {:?}", texture_file)))?;
        let mut expected = String::new();
        texture.read_to_string(&mut expected)?;

        assert_eq!(ret.trim(), expected.trim());

        Ok(())
    }

    pub fn is_mounted(&self) -> Result<bool> {
        let ret = exec(format!("cat /proc/mounts").as_str(), true)?;
        for line in ret.split("\n") {
            if line.contains(self.mount_path.to_str().unwrap()) {
                return Ok(true);
            }
        }
        Ok(false)
    }

    pub fn stop(&self) {
        exec(format!("umount {:?}", self.mount_path).as_str(), false).unwrap();
    }
}
