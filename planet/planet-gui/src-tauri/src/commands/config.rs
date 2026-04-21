//! 配置相关命令

use serde::{Deserialize, Serialize};
use std::path::PathBuf;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GuiConfig {
    /// Controller 服务器地址
    pub controller: String,
    /// 设备 ID
    pub device_id: Option<String>,
    /// TUN 设备名称
    pub tun_name: String,
    /// 心跳间隔（秒）
    pub heartbeat_interval: u64,
    /// 日志级别
    pub log_level: String,
}

impl Default for GuiConfig {
    fn default() -> Self {
        Self {
            controller: "https://controller.ytw.fr:8443".to_string(),
            device_id: None,
            tun_name: "planet0".to_string(),
            heartbeat_interval: 30,
            log_level: "info".to_string(),
        }
    }
}

/// 获取当前配置
#[tauri::command]
pub async fn get_config() -> Result<GuiConfig, String> {
    tracing::info!("获取配置");

    // 从文件加载或返回默认配置
    let config_path = get_config_path();

    if config_path.exists() {
        let content = std::fs::read_to_string(&config_path)
            .map_err(|e| format!("读取配置失败: {}", e))?;
        serde_json::from_str(&content)
            .map_err(|e| format!("解析配置失败: {}", e))
    } else {
        Ok(GuiConfig::default())
    }
}

/// 保存配置
#[tauri::command]
pub async fn save_config(config: GuiConfig) -> Result<(), String> {
    tracing::info!("保存配置: {:?}", config);

    let config_path = get_config_path();

    // 确保目录存在
    if let Some(parent) = config_path.parent() {
        std::fs::create_dir_all(parent)
            .map_err(|e| format!("创建配置目录失败: {}", e))?;
    }

    let content = serde_json::to_string_pretty(&config)
        .map_err(|e| format!("序列化配置失败: {}", e))?;

    std::fs::write(&config_path, content)
        .map_err(|e| format!("写入配置失败: {}", e))?;

    tracing::info!("配置已保存到 {:?}", config_path);
    Ok(())
}

fn get_config_path() -> PathBuf {
    dirs::config_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join("shanlian")
        .join("planet")
        .join("config.json")
}
