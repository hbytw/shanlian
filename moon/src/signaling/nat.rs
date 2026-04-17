//! NAT 类型检测
//!
//! 支持的 NAT 类型：
//! - Full Cone: 任何外部主机都可以通过打洞访问
//! - Restricted Cone: 只有内部发送过的 IP 可以访问
//! - Port Restricted Cone: 只有内部发送过的 IP:Port 可以访问
//! - Symmetric: 每次连接分配不同端口，无法 UDP 打洞

use std::net::SocketAddr;

/// NAT 类型
#[derive(Debug, Clone, Copy, PartialEq)]
pub enum NatType {
    /// 开放互联网，无 NAT
    Open,
    /// 全锥形 NAT
    FullCone,
    /// 限制锥形 NAT
    RestrictedCone,
    /// 端口限制锥形 NAT
    PortRestrictedCone,
    /// 对称 NAT
    Symmetric,
    /// 未知
    Unknown,
}

impl NatType {
    pub fn can_p2p(&self) -> bool {
        matches!(
            self,
            NatType::Open | NatType::FullCone | NatType::RestrictedCone | NatType::PortRestrictedCone
        )
    }

    pub fn description(&self) -> &'static str {
        match self {
            NatType::Open => "开放互联网",
            NatType::FullCone => "全锥形 NAT",
            NatType::RestrictedCone => "限制锥形 NAT",
            NatType::PortRestrictedCone => "端口限制锥形 NAT",
            NatType::Symmetric => "对称 NAT (需要中继)",
            NatType::Unknown => "未知",
        }
    }
}

/// NAT 检测结果
#[derive(Debug, Clone)]
pub struct NatDetectionResult {
    pub nat_type: NatType,
    pub local_addr: SocketAddr,
    pub public_addr: Option<SocketAddr>,
    pub is_symmetric_port: bool,
}

impl NatDetectionResult {
    pub fn new(nat_type: NatType) -> Self {
        Self {
            nat_type,
            local_addr: "0.0.0.0:0".parse().unwrap(),
            public_addr: None,
            is_symmetric_port: false,
        }
    }

    pub fn with_public_addr(mut self, addr: SocketAddr) -> Self {
        self.public_addr = Some(addr);
        self
    }

    pub fn can_p2p(&self) -> bool {
        self.nat_type.can_p2p()
    }

    pub fn requires_relay(&self) -> bool {
        self.nat_type == NatType::Symmetric || self.nat_type == NatType::Unknown
    }
}

/// NAT 检测器
pub struct NatDetector {
    stun_servers: Vec<String>,
}

impl NatDetector {
    pub fn new(stun_servers: Vec<String>) -> Self {
        Self { stun_servers }
    }

    /// 检测本地 NAT 类型
    ///
    /// 流程：
    /// 1. 发送 STUN Binding Request 到第一个服务器
    /// 2. 比较本地地址和返回的公网地址
    /// 3. 如果相同，则无 NAT
    /// 4. 如果不同，尝试第二个服务器判断是否对称 NAT
    pub async fn detect(&self, local_addr: SocketAddr) -> NatDetectionResult {
        if self.stun_servers.is_empty() {
            return NatDetectionResult::new(NatType::Unknown);
        }

        let first_server = &self.stun_servers[0];

        // 发送 STUN Binding Request
        match self.send_stun_request(first_server, local_addr).await {
            Ok(public_addr) => {
                if public_addr.port() == local_addr.port()
                    && public_addr.ip() == local_addr.ip()
                {
                    // No NAT detected
                    return NatDetectionResult::new(NatType::Open)
                        .with_public_addr(public_addr);
                }

                // 有 NAT，检测是否对称
                if self.stun_servers.len() > 1 {
                    let second_server = &self.stun_servers[1];
                    match self.send_stun_request(second_server, local_addr).await {
                        Ok(public_addr2) => {
                            if public_addr.port() != public_addr2.port()
                                || public_addr.ip() != public_addr2.ip()
                            {
                                return NatDetectionResult::new(NatType::Symmetric)
                                    .with_public_addr(public_addr);
                            }

                            // 端口一致但 IP 不同，可能是限制锥形
                            return NatDetectionResult::new(NatType::PortRestrictedCone)
                                .with_public_addr(public_addr);
                        }
                        Err(_) => {
                            return NatDetectionResult::new(NatType::Unknown)
                                .with_public_addr(public_addr);
                        }
                    }
                }

                NatDetectionResult::new(NatType::RestrictedCone).with_public_addr(public_addr)
            }
            Err(_) => NatDetectionResult::new(NatType::Unknown),
        }
    }

    async fn send_stun_request(
        &self,
        server: &str,
        _local_addr: SocketAddr,
    ) -> Result<SocketAddr, std::io::Error> {
        // STUN 服务器地址解析
        let addr: SocketAddr = server.parse().map_err(|_| {
            std::io::Error::new(std::io::ErrorKind::InvalidInput, "Invalid STUN server address")
        })?;

        // 创建 UDP socket
        let socket = std::net::UdpSocket::bind("0.0.0.0:0")?;

        // STUN Binding Request 消息
        // | Type (2B) | Length (2B) | Magic Cookie (4B) | Transaction ID (12B) |
        let mut request = vec![0u8; 20];
        request[0..2].copy_from_slice(&0x0001u16.to_be_bytes()); // Binding Request
        request[2..4].copy_from_slice(&0u16.to_be_bytes()); // Length = 0
        request[4..8].copy_from_slice(&0x2112A442u32.to_be_bytes()); // Magic Cookie
        // Transaction ID = random
        let tid = uuid::Uuid::new_v4();
        request[8..20].copy_from_slice(&tid.as_bytes()[0..12]);

        socket.send_to(&request, addr)?;

        let mut response = vec![0u8; 1024];
        let (len, _) = socket.recv_from(&mut response)?;

        if len < 20 {
            return Err(std::io::Error::new(
                std::io::ErrorKind::UnexpectedEof,
                "STUN response too short",
            ));
        }

        // 解析 XOR-MAPPED-ADDRESS
        self.parse_xor_mapped_address(&response)
    }

    fn parse_xor_mapped_address(&self, response: &[u8]) -> Result<SocketAddr, std::io::Error> {
        // 查找 XOR-MAPPED-ADDRESS 属性 (0x0020)
        let mut offset = 20; // Skip header

        while offset + 4 < response.len() {
            let attr_type = u16::from_be_bytes([response[offset], response[offset + 1]]);
            let attr_len = u16::from_be_bytes([response[offset + 2], response[offset + 3]]) as usize;

            if attr_type == 0x0020 && attr_len == 8 {
                // XOR-MAPPED-ADDRESS found
                let port_offset = offset + 6;
                let port = u16::from_be_bytes([response[port_offset], response[port_offset + 1]])
                    ^ (0x2112A442u32 >> 16) as u16;

                let ip_offset = port_offset + 2;
                let ip_bytes = [
                    response[ip_offset] ^ 0x21,
                    response[ip_offset + 1] ^ 0x12,
                    response[ip_offset + 2] ^ 0xA4,
                    response[ip_offset + 3] ^ 0x42,
                ];
                let ip = std::net::Ipv4Addr::from(ip_bytes);

                return Ok(SocketAddr::new(ip.into(), port));
            }

            offset += 4 + attr_len;
            // Align to 4 bytes
            offset = (offset + 3) & !3;
        }

        Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            "XOR-MAPPED-ADDRESS not found",
        ))
    }
}

impl Default for NatDetector {
    fn default() -> Self {
        Self::new(vec![
            "1.1.1.1:3478".to_string(),   // Cloudflare
            "8.8.8.8:3478".to_string(),    // Google
            "stun.l.google.com:19302".to_string(), // Google STUN
        ])
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_nat_type_p2p() {
        assert!(NatType::Open.can_p2p());
        assert!(NatType::FullCone.can_p2p());
        assert!(NatType::RestrictedCone.can_p2p());
        assert!(NatType::PortRestrictedCone.can_p2p());
        assert!(!NatType::Symmetric.can_p2p());
        assert!(!NatType::Unknown.can_p2p());
    }

    #[test]
    fn test_nat_detection_result() {
        let result = NatDetectionResult::new(NatType::FullCone);
        assert!(result.can_p2p());
        assert!(!result.requires_relay());
    }
}
