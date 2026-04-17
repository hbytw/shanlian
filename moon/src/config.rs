//! Moon 节点配置

use serde::{Deserialize, Serialize};

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Config {
    /// Controller 服务器地址
    pub controller_url: String,

    /// Moon 域名
    pub domain: String,

    /// 公网 IP (可选，自动检测)
    pub public_ip: String,

    /// 网关 IP (VPN 内部网络网关)
    pub gateway_ip: String,

    /// TCP 中继端口
    pub relay_port: u16,

    /// UDP 端口 (STUN)
    pub udp_port: u16,

    /// WSS 中继端口
    pub wss_port: u16,

    /// 健康检查端口
    pub health_port: u16,

    /// API Key
    pub api_key: Option<String>,

    /// API Secret
    pub api_secret: Option<String>,

    /// 跳过注册（仅用于开发测试）
    pub skip_register: bool,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            controller_url: "https://controller.ytw.fr:8443".to_string(),
            domain: "moon1.ytw.fr".to_string(),
            public_ip: String::new(),
            gateway_ip: "10.0.0.1".to_string(),
            relay_port: 11002,
            udp_port: 11001,
            wss_port: 11443,
            health_port: 11003,
            api_key: None,
            api_secret: None,
            skip_register: false,
        }
    }
}
