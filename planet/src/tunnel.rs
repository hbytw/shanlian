//! 隧道管理模块

use anyhow::Result;

/// 隧道类型
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TunnelType {
    /// UDP 直连 P2P
    Udp,
    /// TCP 直连 P2P
    Tcp,
    /// WebSocket 安全隧道
    Wss,
    /// Moon 中继
    Moon,
}

impl Default for TunnelType {
    fn default() -> Self {
        Self::Moon
    }
}

impl std::fmt::Display for TunnelType {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            TunnelType::Udp => write!(f, "UDP"),
            TunnelType::Tcp => write!(f, "TCP"),
            TunnelType::Wss => write!(f, "WSS"),
            TunnelType::Moon => write!(f, "MOON"),
        }
    }
}

/// 隧道信息
#[derive(Debug)]
pub struct TunnelInfo {
    /// 隧道 ID
    pub id: String,
    /// 隧道类型
    pub tunnel_type: TunnelType,
    /// 远程地址
    pub remote_addr: String,
    /// 状态
    pub status: TunnelStatus,
    /// 创建时间
    pub created_at: std::time::Instant,
    /// 最后活动时间
    pub last_active: std::time::Instant,
    /// 接收字节数
    pub recv_bytes: u64,
    /// 发送字节数
    pub send_bytes: u64,
}

/// 隧道状态
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TunnelStatus {
    Connecting,
    Established,
    Disconnecting,
    Closed,
    Error,
}

/// 隧道管理器
pub struct TunnelManager {
    tunnels: std::collections::HashMap<String, TunnelInfo>,
}

impl TunnelManager {
    pub fn new() -> Self {
        Self {
            tunnels: std::collections::HashMap::new(),
        }
    }

    /// 创建新隧道
    pub async fn create(&mut self, tunnel_type: TunnelType, remote_addr: &str) -> Result<String> {
        let id = uuid::Uuid::new_v4().to_string();
        let tunnel = TunnelInfo {
            id: id.clone(),
            tunnel_type,
            remote_addr: remote_addr.to_string(),
            status: TunnelStatus::Connecting,
            created_at: std::time::Instant::now(),
            last_active: std::time::Instant::now(),
            recv_bytes: 0,
            send_bytes: 0,
        };
        self.tunnels.insert(id.clone(), tunnel);
        tracing::info!("创建隧道 {} ({}) -> {}", id, tunnel_type, remote_addr);
        Ok(id)
    }

    /// 获取隧道
    pub fn get(&self, id: &str) -> Option<&TunnelInfo> {
        self.tunnels.get(id)
    }

    /// 获取所有隧道
    pub fn list(&self) -> Vec<&TunnelInfo> {
        self.tunnels.values().collect()
    }

    /// 关闭隧道
    pub async fn close(&mut self, id: &str) -> Result<()> {
        if let Some(tunnel) = self.tunnels.remove(id) {
            tracing::info!("关闭隧道 {} ({})", id, tunnel.tunnel_type);
        }
        Ok(())
    }

    /// 关闭所有隧道
    pub async fn close_all(&mut self) -> Result<()> {
        for id in self.tunnels.keys().cloned().collect::<Vec<_>>() {
            self.close(&id).await?;
        }
        Ok(())
    }
}

impl Default for TunnelManager {
    fn default() -> Self {
        Self::new()
    }
}
