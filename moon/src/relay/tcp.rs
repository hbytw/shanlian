//! TCP 中继服务器
//!
//! 负责：
//! - TCP 11002 端口流量中继
//! - Moon 注册/心跳处理（同一端口复用）

use tokio::net::{TcpListener, TcpStream};
use tokio::io::{AsyncReadExt, AsyncWriteExt};

pub async fn start_tcp_relay(port: u16) -> anyhow::Result<()> {
    let addr = format!("0.0.0.0:{}", port);
    let listener = TcpListener::bind(&addr).await?;

    tracing::info!("TCP 中继服务器启动，监听端口 {}", port);

    loop {
        match listener.accept().await {
            Ok((socket, addr)) => {
                tracing::debug!("收到 TCP 连接: {}", addr);
                tokio::spawn(handle_tcp_connection(socket));
            }
            Err(e) => {
                tracing::error!("接受 TCP 连接失败: {}", e);
            }
        }
    }
}

async fn handle_tcp_connection(socket: TcpStream) {
    let mut buf = vec![0u8; 65536];
    let mut socket = socket;

    loop {
        match socket.read(&mut buf).await {
            Ok(0) => {
                tracing::debug!("TCP 连接关闭");
                break;
            }
            Ok(n) => {
                tracing::trace!("收到 TCP 数据: {} bytes", n);
                // TODO: 处理 SLNX 协议
                // 1. 解析包头
                // 2. ChaCha20 解密
                // 3. 路由到目标 Planet/Moon
                if let Err(e) = socket.write_all(&buf[..n]).await {
                    tracing::error!("写入 TCP 数据失败: {}", e);
                    break;
                }
            }
            Err(e) => {
                tracing::error!("读取 TCP 数据失败: {}", e);
                break;
            }
        }
    }
}
