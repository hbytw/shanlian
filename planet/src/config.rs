//! 配置文件模块

use anyhow::Result;
use serde::{Deserialize, Serialize};
use std::path::PathBuf;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Config {
    /// Controller 服务器地址
    pub controller: String,

    /// 设备 ID
    pub device_id: Option<String>,

    /// 私钥路径
    pub private_key_path: PathBuf,

    /// 公钥路径
    pub public_key_path: PathBuf,

    /// TUN 设备名称
    pub tun_name: String,

    /// TUN MTU
    pub tun_mtu: u32,

    /// 心跳间隔（秒）
    pub heartbeat_interval: u64,

    /// 连接超时（秒）
    pub connect_timeout: u64,

    /// 日志目录
    pub log_dir: PathBuf,

    /// 数据目录
    pub data_dir: PathBuf,

    /// 服务器 ID (从可执行文件名解析)
    pub server_id: Option<String>,
}

impl Default for Config {
    fn default() -> Self {
        let data_dir = dirs::data_local_dir()
            .unwrap_or_else(|| PathBuf::from("."))
            .join("shanlian");

        let log_dir = dirs::data_local_dir()
            .unwrap_or_else(|| PathBuf::from("."))
            .join("shanlian")
            .join("logs");

        let config_dir = data_dir.join("planet");

        Self {
            controller: "https://controller.ytw.fr:8443".to_string(),
            device_id: None,
            private_key_path: config_dir.join("identity"),
            public_key_path: config_dir.join("identity.pub"),
            tun_name: "planet0".to_string(),
            tun_mtu: 1400,
            heartbeat_interval: 30,
            connect_timeout: 30,
            log_dir,
            data_dir,
            server_id: None,
        }
    }
}

impl Config {
    /// 从指定路径加载配置
    pub fn load(path: Option<PathBuf>) -> Result<Self> {
        let mut config = Self::default();

        if let Some(path) = path {
            if path.exists() {
                let content = std::fs::read_to_string(&path)?;
                config = serde_yaml::from_str(&content)?;
                tracing::info!("从 {} 加载配置", path.display());
            }
        }

        // 从环境变量覆盖
        if let Ok(controller) = std::env::var("PLANET_CONTROLLER") {
            config.controller = controller;
        }

        Ok(config)
    }

    /// 保存配置到文件
    pub fn save(&self, path: &PathBuf) -> Result<()> {
        let content = serde_yaml::to_string(self)?;
        std::fs::create_dir_all(path.parent().unwrap_or(&PathBuf::from(".")))?;
        std::fs::write(path, content)?;
        tracing::info!("配置保存到 {}", path.display());
        Ok(())
    }

    /// 从可执行文件名解析服务器 ID
    /// 例如: planet_ytw_fr.exe -> ytw.fr
    pub fn parse_server_id_from_filename(filename: &str) -> Option<String> {
        // 去掉扩展名
        let binding = PathBuf::from(filename);
        let name = binding
            .file_stem()?
            .to_str()?;

        // 格式: planet_ytw_fr -> planet.ytw.fr
        let parts: Vec<&str> = name.split('_').collect();
        if parts.len() >= 3 && parts[0] == "planet" {
            return Some(format!("{}.{}.{}", parts[1], parts[2], parts.get(3).unwrap_or(&"fr")));
        }

        None
    }
}
