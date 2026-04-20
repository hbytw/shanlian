//! 隧道管理模块
//!
//! 负责建立和管理与 Moon 或其他 Planet 的隧道

use anyhow::{Context, Result};
use std::sync::Arc;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpStream, UdpSocket};
use tokio::sync::{mpsc, RwLock};
use url::Url;
use std::time::Duration;

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

impl TunnelType {
    /// 从字符串解析隧道类型
    pub fn from_str(s: &str) -> Option<Self> {
        match s.to_lowercase().as_str() {
            "udp" => Some(TunnelType::Udp),
            "tcp" => Some(TunnelType::Tcp),
            "wss" => Some(TunnelType::Wss),
            "moon" => Some(TunnelType::Moon),
            _ => None,
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
    /// 延迟 (ms)
    pub rtt_ms: Option<u32>,
    /// 丢包率
    pub loss_rate: Option<f32>,
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

/// 隧道处理器接口
pub trait TunnelHandler: Send + Sync {
    /// 连接到远程
    fn connect(&self, remote_addr: &str) -> impl std::future::Future<Output = Result<()>> + Send;
    /// 发送数据
    fn send(&self, data: &[u8]) -> impl std::future::Future<Output = Result<usize>> + Send;
    /// 接收数据
    fn recv(&self, buf: &mut [u8]) -> impl std::future::Future<Output = Result<usize>> + Send;
    /// 关闭连接
    fn close(&self) -> impl std::future::Future<Output = Result<()>> + Send;
}

/// UDP 隧道处理器
pub struct UdpTunnelHandler {
    socket: Option<Arc<UdpSocket>>,
    remote_addr: String,
}

impl UdpTunnelHandler {
    pub fn new(remote_addr: &str) -> Self {
        Self {
            socket: None,
            remote_addr: remote_addr.to_string(),
        }
    }

    pub async fn connect(&mut self) -> Result<()> {
        let socket = UdpSocket::bind("0.0.0.0:0")
            .await
            .context("UDP socket 绑定失败")?;

        socket.connect(&self.remote_addr)
            .await
            .context("UDP 连接失败")?;

        socket.set_read_timeout(Some(Duration::from_secs(30)))
            .context("设置读取超时失败")?;

        self.socket = Some(Arc::new(socket));
        tracing::info!("UDP 隧道连接到 {}", self.remote_addr);
        Ok(())
    }
}

impl TunnelHandler for UdpTunnelHandler {
    async fn connect(&self, _remote_addr: &str) -> Result<()> {
        Ok(())
    }

    async fn send(&self, data: &[u8]) -> Result<usize> {
        match &self.socket {
            Some(socket) => {
                let n = socket.send(data).await?;
                Ok(n)
            }
            None => anyhow::bail!("UDP 隧道未连接"),
        }
    }

    async fn recv(&self, buf: &mut [u8]) -> Result<usize> {
        match &self.socket {
            Some(socket) => {
                let n = socket.recv(buf).await?;
                Ok(n)
            }
            None => anyhow::bail!("UDP 隧道未连接"),
        }
    }

    async fn close(&self) -> Result<()> {
        // UDP socket 在 drop 时自动关闭
        Ok(())
    }
}

/// TCP 隧道处理器
pub struct TcpTunnelHandler {
    stream: Option<Arc<TcpStream>>,
    remote_addr: String,
}

impl TcpTunnelHandler {
    pub fn new(remote_addr: &str) -> Self {
        Self {
            stream: None,
            remote_addr: remote_addr.to_string(),
        }
    }

    pub async fn connect(&mut self) -> Result<()> {
        let stream = TcpStream::connect(&self.remote_addr)
            .await
            .context("TCP 连接失败")?;

        stream.set_nodelay(true)
            .context("设置 TCP_NODELAY 失败")?;

        self.stream = Some(Arc::new(stream));
        tracing::info!("TCP 隧道连接到 {}", self.remote_addr);
        Ok(())
    }
}

impl TunnelHandler for TcpTunnelHandler {
    async fn connect(&self, _remote_addr: &str) -> Result<()> {
        Ok(())
    }

    async fn send(&self, data: &[u8]) -> Result<usize> {
        match &self.stream {
            Some(stream) => {
                let n = stream.write_all(data).await?;
                Ok(data.len())
            }
            None => anyhow::bail!("TCP 隧道未连接"),
        }
    }

    async fn recv(&self, buf: &mut [u8]) -> Result<usize> {
        match &self.stream {
            Some(stream) => {
                let n = stream.read(buf).await?;
                Ok(n)
            }
            None => anyhow::bail!("TCP 隧道未连接"),
        }
    }

    async fn close(&self) -> Result<()> {
        // TcpStream 在 drop 时自动关闭
        Ok(())
    }
}

/// WSS 隧道处理器
pub struct WssTunnelHandler {
    remote_addr: String,
    // TODO: 实现 WebSocket 连接
}

impl WssTunnelHandler {
    pub fn new(remote_addr: &str) -> Self {
        Self {
            remote_addr: remote_addr.to_string(),
        }
    }
}

impl TunnelHandler for WssTunnelHandler {
    async fn connect(&self, _remote_addr: &str) -> Result<()> {
        // TODO: 实现 WebSocket 连接
        tracing::info!("WSS 隧道连接到 {}", self.remote_addr);
        Ok(())
    }

    async fn send(&self, _data: &[u8]) -> Result<usize> {
        // TODO: 实现 WebSocket 发送
        Ok(0)
    }

    async fn recv(&self, _buf: &mut [u8]) -> Result<usize> {
        // TODO: 实现 WebSocket 接收
        Ok(0)
    }

    async fn close(&self) -> Result<()> {
        Ok(())
    }
}

/// Moon 隧道处理器
pub struct MoonTunnelHandler {
    remote_addr: String,
    tcp_handler: TcpTunnelHandler,
}

impl MoonTunnelHandler {
    pub fn new(remote_addr: &str) -> Self {
        Self {
            remote_addr: remote_addr.to_string(),
            tcp_handler: TcpTunnelHandler::new(remote_addr),
        }
    }

    pub async fn connect(&mut self) -> Result<()> {
        // Moon 使用 TCP 协议
        self.tcp_handler.connect().await
    }
}

impl TunnelHandler for MoonTunnelHandler {
    async fn connect(&self, remote_addr: &str) -> Result<()> {
        tracing::info!("Moon 隧道连接到 {}", remote_addr);
        Ok(())
    }

    async fn send(&self, data: &[u8]) -> Result<usize> {
        self.tcp_handler.send(data).await
    }

    async fn recv(&self, buf: &mut [u8]) -> Result<usize> {
        self.tcp_handler.recv(buf).await
    }

    async fn close(&self) -> Result<()> {
        self.tcp_handler.close().await
    }
}

/// 隧道管理器
pub struct TunnelManager {
    tunnels: std::collections::HashMap<String, TunnelInfo>,
    handlers: std::collections::HashMap<String, Box<dyn TunnelHandler>>,
    active_tunnel_id: Option<String>,
}

impl TunnelManager {
    pub fn new() -> Self {
        Self {
            tunnels: std::collections::HashMap::new(),
            handlers: std::collections::HashMap::new(),
            active_tunnel_id: None,
        }
    }

    /// 创建新隧道
    pub async fn create(
        &mut self,
        tunnel_type: TunnelType,
        remote_addr: &str,
    ) -> Result<String> {
        let id = uuid::Uuid::new_v4().to_string();

        // 创建对应的处理器
        let handler: Box<dyn TunnelHandler> = match tunnel_type {
            TunnelType::Udp => Box::new(UdpTunnelHandler::new(remote_addr)),
            TunnelType::Tcp => Box::new(TcpTunnelHandler::new(remote_addr)),
            TunnelType::Wss => Box::new(WssTunnelHandler::new(remote_addr)),
            TunnelType::Moon => Box::new(MoonTunnelHandler::new(remote_addr)),
        };

        let tunnel = TunnelInfo {
            id: id.clone(),
            tunnel_type,
            remote_addr: remote_addr.to_string(),
            status: TunnelStatus::Connecting,
            created_at: std::time::Instant::now(),
            last_active: std::time::Instant::now(),
            recv_bytes: 0,
            send_bytes: 0,
            rtt_ms: None,
            loss_rate: None,
        };

        self.tunnels.insert(id.clone(), tunnel);
        self.handlers.insert(id.clone(), handler);

        tracing::info!("创建隧道 {} ({}) -> {}", id, tunnel_type, remote_addr);
        Ok(id)
    }

    /// 连接隧道
    pub async fn connect(&mut self, id: &str) -> Result<()> {
        // 获取并连接处理器
        if let Some(handler) = self.handlers.get_mut(id) {
            let tunnel = self.tunnels.get(id).unwrap();
            handler.connect(&tunnel.remote_addr).await?;

            // 更新状态
            if let Some(tunnel) = self.tunnels.get_mut(id) {
                tunnel.status = TunnelStatus::Established;
                tunnel.last_active = std::time::Instant::now();
            }

            tracing::info!("隧道 {} 连接成功", id);
        } else {
            anyhow::bail!("隧道 {} 不存在", id);
        }
        Ok(())
    }

    /// 发送数据
    pub async fn send(&mut self, id: &str, data: &[u8]) -> Result<usize> {
        if let Some(handler) = self.handlers.get_mut(id) {
            let n = handler.send(data).await?;
            if let Some(tunnel) = self.tunnels.get_mut(id) {
                tunnel.send_bytes += n as u64;
                tunnel.last_active = std::time::Instant::now();
            }
            Ok(n)
        } else {
            anyhow::bail!("隧道 {} 不存在", id);
        }
    }

    /// 接收数据
    pub async fn recv(&mut self, id: &str, buf: &mut [u8]) -> Result<usize> {
        if let Some(handler) = self.handlers.get_mut(id) {
            let n = handler.recv(buf).await?;
            if let Some(tunnel) = self.tunnels.get_mut(id) {
                tunnel.recv_bytes += n as u64;
                tunnel.last_active = std::time::Instant::now();
            }
            Ok(n)
        } else {
            anyhow::bail!("隧道 {} 不存在", id);
        }
    }

    /// 获取隧道
    pub fn get(&self, id: &str) -> Option<&TunnelInfo> {
        self.tunnels.get(id)
    }

    /// 获取所有隧道
    pub fn list(&self) -> Vec<&TunnelInfo> {
        self.tunnels.values().collect()
    }

    /// 获取活动隧道
    pub fn get_active(&self) -> Option<&TunnelInfo> {
        self.active_tunnel_id
            .as_ref()
            .and_then(|id| self.tunnels.get(id))
    }

    /// 设置活动隧道
    pub fn set_active(&mut self, id: &str) -> Result<()> {
        if !self.tunnels.contains_key(id) {
            anyhow::bail!("隧道 {} 不存在", id);
        }
        self.active_tunnel_id = Some(id.to_string());
        Ok(())
    }

    /// 关闭隧道
    pub async fn close(&mut self, id: &str) -> Result<()> {
        if let Some(handler) = self.handlers.remove(id) {
            handler.close().await?;
        }

        if let Some(tunnel) = self.tunnels.remove(id) {
            tracing::info!("关闭隧道 {} ({})", id, tunnel.tunnel_type);
        }

        if self.active_tunnel_id.as_deref() == Some(id) {
            self.active_tunnel_id = None;
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

    /// 根据隧道类型选择最优隧道
    pub fn select_best_tunnel(&self, tunnel_type: TunnelType) -> Option<String> {
        self.tunnels
            .values()
            .filter(|t| t.tunnel_type == tunnel_type && t.status == TunnelStatus::Established)
            .min_by(|a, b| {
                // 按延迟排序
                a.rtt_ms.unwrap_or(u32::MAX).cmp(&b.rtt_ms.unwrap_or(u32::MAX))
            })
            .map(|t| t.id.clone())
    }
}

impl Default for TunnelManager {
    fn default() -> Self {
        Self::new()
    }
}
