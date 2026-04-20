//! Controller 通信模块

use anyhow::Result;

/// Peer 注册请求
#[derive(Debug, serde::Serialize)]
pub struct PeerRegisterRequest {
    pub device_id: String,
    pub public_key: String,
    pub signature: String,
    pub hostname: String,
    pub os: String,
    pub version: String,
}

/// Peer 注册响应
#[derive(Debug, serde::Deserialize)]
pub struct PeerRegisterResponse {
    pub peer_id: String,
    pub assigned_ip: String,
    pub network_id: String,
    pub online: bool,
}

/// Peer 心跳请求
#[derive(Debug, serde::Serialize)]
pub struct PeerHeartbeatRequest {
    pub device_id: String,
    pub tunnel_type: String,
    pub rtt_ms: u32,
    pub loss_rate: f32,
    pub recv_bytes: u64,
    pub send_bytes: u64,
}

/// Peer 心跳响应
#[derive(Debug, serde::Deserialize)]
pub struct PeerHeartbeatResponse {
    pub status: String,
    pub policy_updates: Vec<PolicyUpdate>,
    pub moon_list: Vec<MoonInfo>,
}

/// 策略更新
#[derive(Debug, serde::Deserialize)]
pub struct PolicyUpdate {
    pub policy_id: String,
    pub action: String,
}

/// Moon 信息
#[derive(Debug, serde::Deserialize)]
pub struct MoonInfo {
    pub domain: String,
    pub relay_port: u16,
    pub wss_port: u16,
    pub weight: u32,
    pub online: bool,
}

/// Controller 客户端
pub struct ControllerClient {
    server_url: String,
    device_id: String,
}

impl ControllerClient {
    pub fn new(server_url: String, device_id: String) -> Self {
        Self { server_url, device_id }
    }

    /// 注册设备到 Controller
    pub async fn register(&self) -> Result<PeerRegisterResponse> {
        // TODO: 实现注册逻辑
        anyhow::bail!("Controller 注册功能开发中")
    }

    /// 发送心跳
    pub async fn heartbeat(&self) -> Result<PeerHeartbeatResponse> {
        // TODO: 实现心跳逻辑
        anyhow::bail!("Controller 心跳功能开发中")
    }

    /// 发现节点地址
    pub async fn discover(&self, peer_id: &str) -> Result<DiscoverResponse> {
        // TODO: 实现发现逻辑
        anyhow::bail!("Controller 发现功能开发中")
    }
}

/// 发现响应
#[derive(Debug, serde::Deserialize)]
pub struct DiscoverResponse {
    pub peer_id: String,
    pub public_ip: String,
    pub port: u16,
    pub nat_type: String,
}
