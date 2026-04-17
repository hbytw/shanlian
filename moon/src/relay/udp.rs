//! UDP STUN 服务器
//!
//! 负责：
//!- UDP 11001 端口 STUN/NAT 穿透辅助

use std::net::SocketAddr;
use tokio::net::UdpSocket;

pub async fn start_udp_stun(port: u16) -> anyhow::Result<()> {
    let addr = format!("0.0.0.0:{}", port);
    let socket = UdpSocket::bind(&addr).await?;

    tracing::info!("UDP STUN 服务器启动，监听端口 {}", port);

    let mut buf = vec![0u8; 65536];
    loop {
        match socket.recv_from(&mut buf).await {
            Ok((len, addr)) => {
                tracing::trace!("收到 UDP 数据: {} bytes from {}", len, addr);
                // TODO: 实现 STUN 协议
                // STUN 消息类型:
                // - Binding Request (0x0001)
                // - Binding Response (0x0101)
                // - Binding Error (0x0111)
                // - Binding Indication (0x0011)
                handle_stun_message(&socket, &buf[..len], addr).await;
            }
            Err(e) => {
                tracing::error!("接收 UDP 数据失败: {}", e);
            }
        }
    }
}

async fn handle_stun_message(socket: &UdpSocket, buf: &[u8], addr: SocketAddr) {
    if buf.len() < 20 {
        return;
    }

    // STUN 消息格式 (简单实现)
    // | Type (2B) | Length (2B) | Magic Cookie (4B) | Transaction ID (12B) |
    let msg_type = u16::from_be_bytes([buf[0], buf[1]]);
    let magic_cookie = u32::from_be_bytes([buf[4], buf[5], buf[6], buf[7]]);

    // 验证 Magic Cookie (0x2112A442)
    if magic_cookie != 0x2112A442 {
        tracing::debug!("无效的 STUN Magic Cookie");
        return;
    }

    match msg_type {
        0x0001 => {
            // Binding Request - 返回公网地址
            tracing::debug!("收到 STUN Binding Request from {}", addr);
            let response = build_binding_response(addr);
            if let Err(e) = socket.send_to(&response, addr).await {
                tracing::error!("发送 STUN Response 失败: {}", e);
            }
        }
        _ => {
            tracing::debug!("未知的 STUN 消息类型: 0x{:04x}", msg_type);
        }
    }
}

/// 构建 Binding Response
fn build_binding_response(addr: SocketAddr) -> Vec<u8> {
    let mut response = Vec::with_capacity(32 + 8);

    // STUN Header
    // Type: Binding Response (0x0101)
    response.extend_from_slice(&0x0101u16.to_be_bytes());
    // Length: 12 (包含 XOR-MAPPED-ADDRESS)
    response.extend_from_slice(&12u16.to_be_bytes());
    // Magic Cookie
    response.extend_from_slice(&0x2112A442u32.to_be_bytes());
    // Transaction ID (使用请求的)
    response.extend_from_slice(&[0; 12]);

    // XOR-MAPPED-ADDRESS 属性
    // Type: 0x0020, Length: 8
    response.extend_from_slice(&0x0020u16.to_be_bytes());
    response.extend_from_slice(&8u16.to_be_bytes());
    // Family: IPv4 (0x01), Port: XOR with magic cookie
    let port = addr.port() ^ (0x2112A442u32 >> 16) as u16;
    response.push(0x01); // IPv4
    response.extend_from_slice(&port.to_be_bytes());
    // Address: XOR with magic cookie
    if let std::net::IpAddr::V4(ipv4) = addr.ip() {
        let ip_bytes = ipv4.octets();
        let xor_ip = u32::from_be_bytes([ip_bytes[0], ip_bytes[1], ip_bytes[2], ip_bytes[3]])
            ^ 0x2112A442;
        response.extend_from_slice(&xor_ip.to_be_bytes());
    }

    response
}
