//! WSS 中继服务器
//!
//! 负责：
//! - TCP 11443 端口 WebSocket 中继
//! - 支持严格 NAT 穿透

use tokio::net::TcpListener;
use tokio_tungstenite::{accept_async, tungstenite::Message};
use futures_util::StreamExt;

pub async fn start_wss_relay(port: u16) -> anyhow::Result<()> {
    let addr = format!("0.0.0.0:{}", port);
    let listener = TcpListener::bind(&addr).await?;

    tracing::info!("WSS 中继服务器启动，监听端口 {}", port);

    loop {
        match listener.accept().await {
            Ok((stream, addr)) => {
                tracing::debug!("收到 WSS 连接: {}", addr);
                tokio::spawn(handle_wss_connection(stream, addr));
            }
            Err(e) => {
                tracing::error!("接受 WSS 连接失败: {}", e);
            }
        }
    }
}

async fn handle_wss_connection(stream: tokio::net::TcpStream, addr: std::net::SocketAddr) {
    match accept_async(stream).await {
        Ok(ws_stream) => {
            tracing::info!("WSS 握手成功: {}", addr);
            process_wss_messages(ws_stream, addr).await;
        }
        Err(e) => {
            tracing::error!("WSS 握手失败: {} - {}", addr, e);
        }
    }
}

async fn process_wss_messages(
    ws_stream: tokio_tungstenite::WebSocketStream<tokio::net::TcpStream>,
    addr: std::net::SocketAddr,
) {
    let (write, mut read) = ws_stream.split();
    let mut write = write;

    // 处理收到的消息
    while let Some(msg_result) = read.next().await {
        match msg_result {
            Ok(Message::Binary(data)) => {
                tracing::trace!("收到 WSS 二进制数据: {} bytes", data.len());
                // TODO: 解析 SLNX 协议帧
                // WSS 帧格式:
                // | Channel ID (2B) | Frame Type (1B) | Length (2B) | Tunnel Packet |
                process_slnx_frame(&data, &mut write, addr).await;
            }
            Ok(Message::Text(text)) => {
                tracing::debug!("收到 WSS 文本消息: {}", text);
            }
            Ok(Message::Close(_)) => {
                tracing::info!("WSS 连接关闭: {}", addr);
                break;
            }
            Ok(_) => {
                // Ignore other message types
            }
            Err(e) => {
                tracing::error!("WSS 读取错误: {}", e);
                break;
            }
        }
    }
}

/// 处理 SLNX 帧
async fn process_slnx_frame<F>(
    data: &[u8],
    write: &mut F,
    addr: std::net::SocketAddr,
) where
    F: futures_util::SinkExt<Message> + Unpin,
{
    if data.len() < 5 {
        tracing::warn!("SLNX 帧太短: {} bytes", data.len());
        return;
    }

    let channel_id = u16::from_be_bytes([data[0], data[1]]);
    let frame_type = data[2];
    let length = u16::from_be_bytes([data[3], data[4]]) as usize;

    if data.len() < 5 + length {
        tracing::warn!("SLNX 帧长度不匹配: header says {} but got {}", length, data.len() - 5);
        return;
    }

    tracing::trace!(
        "SLNX 帧: channel={}, type={}, length={}",
        channel_id, frame_type, length
    );

    match frame_type {
        0x01 => {
            // 隧道包 - 转发到目标 Planet
            // TODO: 实现隧道转发逻辑
        }
        0x02 => {
            // 心跳
            // TODO: 响应心跳
        }
        0x03 => {
            // 关闭 channel
            tracing::info!("收到关闭 channel 命令: {}", channel_id);
        }
        _ => {
            tracing::warn!("未知的 SLNX 帧类型: {}", frame_type);
        }
    }
}
