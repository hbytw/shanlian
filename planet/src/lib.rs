//! 闪连 VPN Planet 核心库
//!
//! Planet 是闪连 VPN 的客户端程序，负责：
//! - 连接 Controller 进行设备注册和认证
//! - 建立到 Moon 或其他 Planet 的隧道
//! - 管理 TUN 虚拟网卡
//! - 执行路由和策略

pub mod audit;
pub mod bond;
pub mod cli;
pub mod config;
pub mod console;
pub mod controller;
pub mod crypto;
pub mod nat;
pub mod policy;
pub mod routing;
pub mod session;
pub mod tunnel;
pub mod tun;
pub mod vpn;

pub use config::Config;
pub use crypto::{KeyPair, Crypto};
pub use tunnel::{TunnelManager, TunnelType};
pub use controller::ControllerClient;
pub use bond::BondManager;
pub use session::SessionManager;
pub use policy::PolicyEngine;
pub use nat::NatHandler;
pub use vpn::{VpnEngine, VpnConfig, VpnStats, VpnEngineHandle};

use serde::{Deserialize, Serialize};

/// 连接状态
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ConnectionStatus {
    pub connected: bool,
    pub server: String,
    pub ip: String,
    pub uptime_secs: u64,
    pub last_error: Option<String>,
}

impl Default for ConnectionStatus {
    fn default() -> Self {
        Self {
            connected: false,
            server: String::new(),
            ip: String::new(),
            uptime_secs: 0,
            last_error: None,
        }
    }
}

/// 隧道信息
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TunnelInfo {
    pub id: String,
    pub tunnel_type: String,
    pub server: String,
    pub latency_ms: u32,
    pub status: String,
}

/// 流量统计
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct TrafficStats {
    pub bytes_sent: u64,
    pub bytes_received: u64,
    pub packets_sent: u64,
    pub packets_received: u64,
}

/// Planet 客户端
pub struct PlanetClient {
    config: Config,
    state: ClientState,
}

struct ClientState {
    connection: ConnectionStatus,
    stats: TrafficStats,
    connected_at: Option<std::time::Instant>,
}

impl PlanetClient {
    pub fn new(config: Config) -> Self {
        Self {
            config,
            state: ClientState {
                connection: ConnectionStatus::default(),
                stats: TrafficStats::default(),
                connected_at: None,
            },
        }
    }

    pub fn config(&self) -> &Config {
        &self.config
    }

    pub fn is_connected(&self) -> bool {
        self.state.connection.connected
    }

    pub fn connection_status(&self) -> ConnectionStatus {
        let mut status = self.state.connection.clone();
        if let Some(connected_at) = self.state.connected_at {
            status.uptime_secs = connected_at.elapsed().as_secs();
        }
        status
    }

    pub async fn connect(&mut self) -> anyhow::Result<()> {
        tracing::info!("正在连接到 Controller: {}", self.config.controller);

        let device_id = self.config.device_id.clone()
            .unwrap_or_else(|| {
                let id = uuid::Uuid::new_v4().to_string();
                tracing::info!("生成新设备 ID: {}", id);
                id
            });

        let mut controller = ControllerClient::new(&self.config.controller, &device_id).await?;

        match controller.register().await {
            Ok(response) => {
                tracing::info!("设备注册成功!");
                tracing::info!("  peer_id: {}", response.peer_id);
                tracing::info!("  assigned_ip: {}", response.assigned_ip);

                self.state.connection.ip = response.assigned_ip;
                self.state.connection.connected = true;
                self.state.connection.server = self.config.controller.clone();
                self.state.connected_at = Some(std::time::Instant::now());

                tracing::info!("连接成功");
            }
            Err(e) => {
                tracing::error!("设备注册失败: {}", e);
                self.state.connection.last_error = Some(e.to_string());
                return Err(e);
            }
        }

        Ok(())
    }

    pub async fn disconnect(&mut self) -> anyhow::Result<()> {
        tracing::info!("断开 VPN 连接");
        self.state.connection.connected = false;
        self.state.connection.ip.clear();
        self.state.connected_at = None;
        Ok(())
    }
}
