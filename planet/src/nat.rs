//! NAT 类型检测和打洞模块
//!
//! 负责 STUN 客户端实现、NAT 类型检测和 UDP 打洞

use anyhow::{Context, Result};
use std::net::{Ipv4Addr, SocketAddr, ToSocketAddrs};
use std::time::Duration;
use tokio::net::UdpSocket;
use tokio::time::timeout;

/// STUN 消息类型
const STUN_BINDING_REQUEST: u16 = 0x0001;
const STUN_BINDING_RESPONSE: u16 = 0x0101;
const STUN_BINDING_ERROR: u16 = 0x0111;

/// STUN 属性类型
const STUN_ATTR_MAPPED_ADDRESS: u16 = 0x0001;
const STUN_ATTR_XOR_MAPPED_ADDRESS: u16 = 0x0020;
const STUN_ATTR_CHANGE_REQUEST: u16 = 0x0003;

/// NAT 类型枚举
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NatType {
    /// 开放网络 - 无 NAT
    Open,
    /// 全锥型 NAT
    FullCone,
    /// 限制锥型 NAT
    RestrictedCone,
    /// 端口限制锥型 NAT
    PortRestrictedCone,
    /// 对称 NAT - 无法打洞
    Symmetric,
}

impl std::fmt::Display for NatType {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            NatType::Open => write!(f, "开放 (无 NAT)"),
            NatType::FullCone => write!(f, "全锥型 NAT"),
            NatType::RestrictedCone => write!(f, "限制锥型 NAT"),
            NatType::PortRestrictedCone => write!(f, "端口限制锥型 NAT"),
            NatType::Symmetric => write!(f, "对称 NAT (无法 P2P)"),
        }
    }
}

impl NatType {
    /// 是否支持 P2P 打洞
    pub fn supports_p2p(&self) -> bool {
        matches!(self, NatType::Open | NatType::FullCone | NatType::RestrictedCone | NatType::PortRestrictedCone)
    }
}

/// NAT 检测结果
#[derive(Debug)]
pub struct NatInfo {
    /// NAT 类型
    pub nat_type: NatType,
    /// 公网 IP
    pub public_ip: String,
    /// 公网端口
    pub public_port: u16,
    /// 本地 IP
    pub local_ip: String,
    /// 本地端口
    pub local_port: u16,
    /// 是否可打洞
    pub hole_punchable: bool,
}

/// STUN 服务器
#[derive(Debug, Clone)]
pub struct StunServer {
    pub host: String,
    pub port: u16,
}

impl StunServer {
    pub fn new(host: &str, port: u16) -> Self {
        Self {
            host: host.to_string(),
            port,
        }
    }
}

/// STUN 事务 ID
#[derive(Debug, Clone)]
struct TransactionId([u8; 12]);

impl TransactionId {
    fn new() -> Self {
        use rand::Rng;
        let mut id = [0u8; 12];
        rand::thread_rng().fill(&mut id);
        Self(id)
    }

    fn as_slice(&self) -> &[u8] {
        &self.0
    }
}

/// NAT 处理器
pub struct NatHandler {
    stun_servers: Vec<StunServer>,
    socket: Option<Arc<UdpSocket>>,
}

use std::sync::Arc;

impl NatHandler {
    pub fn new() -> Self {
        Self {
            stun_servers: vec![
                StunServer::new("stun.qq.com", 3478),
                StunServer::new("stun.l.google.com", 19302),
            ],
            socket: None,
        }
    }

    /// 初始化 UDP socket
    pub async fn init(&mut self) -> Result<()> {
        let socket = UdpSocket::bind("0.0.0.0:0")
            .await
            .context("绑定 UDP socket 失败")?;

        self.socket = Some(Arc::new(socket));
        tracing::info!("NAT 处理器初始化成功");
        Ok(())
    }

    /// 检测 NAT 类型
    pub async fn detect(&mut self) -> Result<NatInfo> {
        if self.socket.is_none() {
            self.init().await?;
        }

        let local_addr = {
            let socket = self.socket.as_ref().unwrap();
            socket.local_addr().context("获取本地地址失败")?
        };

        tracing::info!("开始 NAT 类型检测...");

        // 第一步：获取公网地址
        let (public_ip, public_port) = self.query_public_ip().await?;

        tracing::info!("公网地址: {}:{}", public_ip, public_port);

        // 第二步：测试端口是否保持不变 (检测对称 NAT)
        let (public_ip2, public_port2) = self.query_public_ip_with_different_server().await?;

        let nat_type = if public_ip == public_ip2 && public_port == public_port2 {
            // 端口保持不变，可能是开放或锥型 NAT
            NatType::Open
        } else {
            // 端口变化，对称 NAT
            NatType::Symmetric
        };

        // 第三步：如果不是对称 NAT，尝试进一步分类
        let final_nat_type = if nat_type == NatType::Open {
            // 测试是否有 NAT (通过比较本地和公网地址)
            if local_addr.ip().to_string() == public_ip && local_addr.port() == public_port {
                NatType::Open
            } else {
                // 有 NAT，进一步测试
                self.classify_nat_type(public_ip.as_str(), public_port).await?
            }
        } else {
            nat_type
        };

        let info = NatInfo {
            nat_type: final_nat_type,
            public_ip,
            public_port,
            local_ip: local_addr.ip().to_string(),
            local_port: local_addr.port(),
            hole_punchable: final_nat_type.supports_p2p(),
        };

        tracing::info!("NAT 检测完成: {:?}", info);
        Ok(info)
    }

    /// 向 STUN 服务器查询公网 IP
    async fn query_public_ip(&self) -> Result<(String, u16)> {
        let socket = self.socket.as_ref().context("socket 未初始化")?;

        for server in &self.stun_servers {
            match self.send_binding_request(&server.host, server.port).await {
                Ok((ip, port)) => return Ok((ip, port)),
                Err(e) => {
                    tracing::warn!("STUN 服务器 {} 失败: {}", server.host, e);
                    continue;
                }
            }
        }

        anyhow::bail!("所有 STUN 服务器都失败")
    }

    /// 使用不同的 STUN 服务器查询
    async fn query_public_ip_with_different_server(&self) -> Result<(String, u16)> {
        // 如果有多个服务器，用第二个查询
        if self.stun_servers.len() >= 2 {
            let server = &self.stun_servers[1];
            return self.send_binding_request(&server.host, server.port).await;
        }

        // 否则使用不同的地址
        self.send_binding_request("stun.xtels.cn", 3478).await
    }

    /// 发送 STUN Binding Request
    async fn send_binding_request(
        &self,
        server_host: &str,
        server_port: u16,
    ) -> Result<(String, u16)> {
        let socket = self.socket.as_ref().context("socket 未初始化")?;

        let server_addr = format!("{}:{}", server_host, server_port)
            .to_socket_addrs()?
            .next()
            .context("解析 STUN 服务器地址失败")?;

        // 构建 STUN Binding Request
        let transaction_id = TransactionId::new();
        let request = self.build_binding_request(&transaction_id);

        // 发送请求
        socket.send_to(&request, server_addr)
            .await
            .context("发送 STUN 请求失败")?;

        // 接收响应
        let mut response_buf = [0u8; 1024];
        let (n, _) = timeout(Duration::from_secs(5), socket.recv_from(&mut response_buf))
            .await
            .map_err(|_| anyhow::anyhow!("STUN 响应超时"))??;

        // 解析响应
        self.parse_binding_response(&response_buf[..n], &transaction_id)
    }

    /// 构建 STUN Binding Request
    fn build_binding_request(&self, transaction_id: &TransactionId) -> Vec<u8> {
        let mut msg = Vec::with_capacity(20 + 4);

        // Message Type: Binding Request
        msg.extend_from_slice(&STUN_BINDING_REQUEST.to_be_bytes());
        // Message Length (不含 Header)
        msg.extend_from_slice(&(4u16).to_be_bytes()); // 只有 Transaction ID
        // Transaction ID
        msg.extend_from_slice(transaction_id.as_slice());

        msg
    }

    /// 解析 STUN Binding Response
    fn parse_binding_response(
        &self,
        data: &[u8],
        expected_transaction: &TransactionId,
    ) -> Result<(String, u16)> {
        if data.len() < 20 {
            anyhow::bail!("STUN 响应太短");
        }

        let msg_type = u16::from_be_bytes([data[0], data[1]]);
        if msg_type != STUN_BINDING_RESPONSE {
            anyhow::bail!("不是 Binding Response: {:04x}", msg_type);
        }

        // 验证 Transaction ID
        let recv_transaction = &data[4..16];
        if recv_transaction != expected_transaction.as_slice() {
            anyhow::bail!("Transaction ID 不匹配");
        }

        // 解析属性
        let mut offset = 20;
        let mut mapped_addr: Option<(String, u16)> = None;

        while offset + 4 <= data.len() {
            let attr_type = u16::from_be_bytes([data[offset], data[offset + 1]]);
            let attr_length = u16::from_be_bytes([data[offset + 2], data[offset + 3]]) as usize;

            if offset + 4 + attr_length > data.len() {
                break;
            }

            let attr_data = &data[offset + 4..offset + 4 + attr_length];

            match attr_type {
                STUN_ATTR_MAPPED_ADDRESS => {
                    if attr_length >= 8 && attr_data[1] == 0x01 {
                        // IPv4
                        let ip = format!(
                            "{}.{}.{}.{}",
                            attr_data[4], attr_data[5], attr_data[6], attr_data[7]
                        );
                        let port = u16::from_be_bytes([attr_data[2], attr_data[3]]);
                        mapped_addr = Some((ip, port));
                    }
                }
                STUN_ATTR_XOR_MAPPED_ADDRESS => {
                    if attr_length >= 8 && attr_data[1] == 0x01 {
                        // IPv4 with XOR
                        let xored = [
                            attr_data[4] ^ 0x21,
                            attr_data[5] ^ 0x42,
                            attr_data[6] ^ 0x43,
                            attr_data[7] ^ 0x54,
                        ];
                        let ip = format!("{}.{}.{}.{}", xored[0], xored[1], xored[2], xored[3]);
                        let port = u16::from_be_bytes([attr_data[2], attr_data[3]]) ^ 0x2112;
                        mapped_addr = Some((ip, port));
                    }
                }
                _ => {}
            }

            offset += 4 + attr_length;
            // 属性需要 4 字节对齐
            offset += (4 - (attr_length % 4)) % 4;
        }

        mapped_addr.ok_or_else(|| anyhow::anyhow!("未找到 MAPPED-ADDRESS"))
    }

    /// 根据多次测试结果分类 NAT 类型
    async fn classify_nat_type(
        &mut self,
        public_ip: &str,
        public_port: u16,
    ) -> Result<NatType> {
        // 简化实现：返回全锥型 NAT
        // 实际需要多次测试不同地址和端口
        tracing::info!("NAT 类型分类: 全锥型 NAT (简化实现)");
        Ok(NatType::FullCone)
    }

    /// UDP 打洞
    pub async fn punch_hole(&self, target_ip: &str, target_port: u16) -> Result<()> {
        let socket = self.socket.as_ref().context("socket 未初始化")?;

        let target_addr = format!("{}:{}", target_ip, target_port)
            .to_socket_addrs()?
            .next()
            .context("解析目标地址失败")?;

        tracing::info!("开始 UDP 打洞: {}:{}", target_ip, target_port);

        // 发送多个 UDP 包到目标
        let hole_packet = b"HOLE_PUNCH";
        for i in 0..5 {
            if let Err(e) = socket.send_to(hole_packet, target_addr).await {
                tracing::warn!("打洞包 {} 发送失败: {}", i, e);
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        }

        tracing::info!("UDP 打洞请求已发送");
        Ok(())
    }

    /// 执行 UDP 打洞流程 (需要双方配合)
    pub async fn perform_punch_through(
        &self,
        target_ip: &str,
        target_port: u16,
    ) -> Result<std::net::SocketAddr> {
        let socket = self.socket.as_ref().context("socket 未初始化")?;
        let target_addr = format!("{}:{}", target_ip, target_port)
            .to_socket_addrs()?
            .next()
            .context("解析目标地址失败")?;

        tracing::info!("开始 UDP 打洞流程 -> {}:{}", target_ip, target_port);

        // 第一阶段：向目标发送打洞包
        let punch_packet = b"PUNCH";
        socket.send_to(punch_packet, target_addr).await?;

        // 第二阶段：等待对方回复
        let mut buf = [0u8; 1024];
        let (n, from) = timeout(Duration::from_secs(10), socket.recv_from(&mut buf))
            .await
            .context("等待打洞响应超时")??;

        let response = &buf[..n];
        if response == b"PUNCH_ACK" {
            tracing::info!("打洞成功: {}", from);
            Ok(from)
        } else {
            anyhow::bail!("收到未知打洞响应: {:?}", response);
        }
    }
}

impl Default for NatHandler {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_nat_type_display() {
        assert_eq!(NatType::Open.to_string(), "开放 (无 NAT)");
        assert_eq!(NatType::FullCone.to_string(), "全锥型 NAT");
        assert_eq!(NatType::Symmetric.to_string(), "对称 NAT (无法 P2P)");
    }

    #[test]
    fn test_nat_type_supports_p2p() {
        assert!(NatType::Open.supports_p2p());
        assert!(NatType::FullCone.supports_p2p());
        assert!(NatType::RestrictedCone.supports_p2p());
        assert!(NatType::PortRestrictedCone.supports_p2p());
        assert!(!NatType::Symmetric.supports_p2p()); // Symmetric does NOT support P2P
    }

    #[test]
    fn test_nat_info_creation() {
        let info = NatInfo {
            nat_type: NatType::FullCone,
            public_ip: "1.2.3.4".to_string(),
            public_port: 12345,
            local_ip: "192.168.1.100".to_string(),
            local_port: 8080,
            hole_punchable: true,
        };

        assert_eq!(info.nat_type, NatType::FullCone);
        assert_eq!(info.public_ip, "1.2.3.4");
        assert_eq!(info.public_port, 12345);
        assert!(info.hole_punchable);
    }

    #[test]
    fn test_nat_handler_new() {
        let handler = NatHandler::new();
        // Newly created handler should not have socket initialized
        assert!(handler.socket.is_none());
    }
}
