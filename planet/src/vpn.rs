//! VPN 引擎模块
//!
//! 核心 VPN 数据平面，负责：
//! - TUN 设备与隧道之间的数据包转发
//! - SLNX 协议封装/解封装
//! - 心跳保活
//! - 优雅关闭

use crate::tunnel::{make_data_packet, make_heartbeat_packet, make_close_packet, parse_packet, TunnelManager};
use crate::tun::TunDevice;
use anyhow::Result;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::RwLock;
use tokio::time::sleep;
use tracing::{error, info, warn};

/// VPN 引擎配置
#[derive(Debug, Clone)]
pub struct VpnConfig {
    pub heartbeat_interval_secs: u64,
    pub tun_mtu: u32,
    pub tun_name: String,
    pub packet_buf_size: usize,
}

impl Default for VpnConfig {
    fn default() -> Self {
        Self {
            heartbeat_interval_secs: 30,
            tun_mtu: 1400,
            tun_name: "planet0".to_string(),
            packet_buf_size: 65535,
        }
    }
}

/// VPN 统计信息
#[derive(Debug, Default, Clone)]
pub struct VpnStats {
    pub packets_sent: u64,
    pub packets_received: u64,
    pub bytes_sent: u64,
    pub bytes_received: u64,
    pub errors: u64,
    pub tunnel_rtt_ms: Option<u32>,
}

/// VPN 引擎
#[derive(Clone)]
pub struct VpnEngine {
    tun: Arc<TunDevice>,
    tunnel: Arc<RwLock<TunnelManager>>,
    session_id: [u8; 16],
    shutdown_flag: Arc<std::sync::atomic::AtomicBool>,
    config: VpnConfig,
}

impl VpnEngine {
    pub async fn new(tun: TunDevice, tunnel: TunnelManager, config: VpnConfig) -> Result<Self> {
        let session_id = *uuid::Uuid::new_v4().as_bytes();
        let shutdown_flag = Arc::new(std::sync::atomic::AtomicBool::new(false));

        Ok(Self {
            tun: Arc::new(tun),
            tunnel: Arc::new(RwLock::new(tunnel)),
            session_id,
            shutdown_flag,
            config,
        })
    }

    /// 请求关闭
    pub fn shutdown(&self) {
        self.shutdown_flag.store(true, std::sync::atomic::Ordering::SeqCst);
    }

    /// 检查是否已请求关闭
    fn is_shutting_down(&self) -> bool {
        self.shutdown_flag.load(std::sync::atomic::Ordering::SeqCst)
    }

    /// 运行 VPN 数据平面
    pub async fn run(&self) -> Result<()> {
        info!("VPN 引擎启动，会话 ID: {:?}", &self.session_id[..8]);

        let tun = self.tun.clone();
        let tunnel = self.tunnel.clone();
        let session_id = self.session_id;
        let config = self.config.clone();
        let shutdown = self.shutdown_flag.clone();

        // TUN → 隧道 任务
        let tun_to_tun_task = {
            let tun = tun.clone();
            let tunnel = tunnel.clone();
            let shutdown = shutdown.clone();
            tokio::spawn(async move {
                let mut buf = vec![0u8; config.packet_buf_size];

                loop {
                    if shutdown.load(std::sync::atomic::Ordering::SeqCst) {
                        info!("TUN→隧道 循环退出");
                        break;
                    }

                    match tun.read_packet(&mut buf).await {
                        Ok(n) if n > 0 => {
                            let packet = make_data_packet(&session_id, &buf[..n]);
                            let mut t = tunnel.write().await;
                            if let Some(tid) = t.get_active().map(|t| t.id.clone()) {
                                if let Err(e) = t.send(&tid, &packet).await {
                                    warn!("隧道发送失败: {}", e);
                                }
                            }
                        }
                        Ok(_) => {
                            sleep(Duration::from_millis(1)).await;
                        }
                        Err(e) => {
                            let err_str = e.to_string();
                            if err_str.contains("WouldBlock") || err_str.contains("temporarily unavailable") {
                                sleep(Duration::from_millis(1)).await;
                            } else {
                                warn!("TUN 读取错误: {}", e);
                                sleep(Duration::from_millis(10)).await;
                            }
                        }
                    }
                }
            })
        };

        // 隧道 → TUN 任务
        let tun_to_tun_recv = {
            let tun = tun.clone();
            let tunnel = tunnel.clone();
            let shutdown = shutdown.clone();
            tokio::spawn(async move {
                let mut buf = vec![0u8; config.packet_buf_size];

                loop {
                    if shutdown.load(std::sync::atomic::Ordering::SeqCst) {
                        info!("隧道→TUN 循环退出");
                        break;
                    }

                    sleep(Duration::from_millis(1)).await;

                    let mut t = tunnel.write().await;
                    if let Some(tid) = t.get_active().map(|t| t.id.clone()) {
                        match t.recv(&tid, &mut buf).await {
                            Ok(n) if n > 0 => {
                                drop(t);
                                if let Ok((header, payload)) = parse_packet(&buf[..n]) {
                                    if header.flags & 0x02 != 0 && !payload.is_empty() {
                                        if let Err(e) = tun.write_packet(&payload).await {
                                            warn!("TUN 写入错误: {}", e);
                                        }
                                    }
                                }
                            }
                            Err(e) => {
                                warn!("隧道接收错误: {}", e);
                                sleep(Duration::from_millis(10)).await;
                            }
                            _ => {}
                        }
                    }
                }
            })
        };

        // 心跳任务
        let heartbeat_task = {
            let tunnel = tunnel.clone();
            let shutdown = shutdown.clone();
            tokio::spawn(async move {
                let interval = Duration::from_secs(config.heartbeat_interval_secs);

                loop {
                    if shutdown.load(std::sync::atomic::Ordering::SeqCst) {
                        info!("心跳任务退出");
                        break;
                    }

                    sleep(interval).await;

                    let packet = make_heartbeat_packet(&session_id);
                    let mut t = tunnel.write().await;
                    if let Some(tid) = t.get_active().map(|t| t.id.clone()) {
                        if let Err(e) = t.send(&tid, &packet).await {
                            warn!("心跳发送失败: {}", e);
                        } else {
                            info!("心跳包已发送");
                        }
                    }
                }
            })
        };

        // 等待所有任务
        let _ = tun_to_tun_task.await;
        let _ = tun_to_tun_recv.await;
        let _ = heartbeat_task.await;

        // 关闭
        self.shutdown_internal().await
    }

    /// 内部关闭逻辑
    async fn shutdown_internal(&self) -> Result<()> {
        info!("VPN 引擎关闭中...");

        let close_packet = make_close_packet(&self.session_id);
        let mut t = self.tunnel.write().await;
        if let Some(tid) = t.get_active().map(|t| t.id.clone()) {
            let _ = t.send(&tid, &close_packet).await;
        }
        if let Err(e) = t.close_all().await {
            error!("关闭隧道失败: {}", e);
        }
        drop(t);

        // 关闭 TUN 设备
        if let Ok(tun) = Arc::try_unwrap(self.tun.clone()) {
            if let Err(e) = tun.close().await {
                warn!("关闭 TUN 设备失败: {}", e);
            }
        } else {
            warn!("TUN 设备引用计数 > 1，跳过关闭");
        }

        info!("VPN 引擎已关闭");
        Ok(())
    }
}

/// VPN 引擎句柄
pub struct VpnEngineHandle {
    engine: Arc<RwLock<Option<VpnEngine>>>,
    task: tokio::task::JoinHandle<Result<()>>,
}

impl VpnEngineHandle {
    pub fn new(engine: VpnEngine, task: tokio::task::JoinHandle<Result<()>>) -> Self {
        Self {
            engine: Arc::new(RwLock::new(Some(engine))),
            task,
        }
    }

    pub async fn shutdown(&self) {
        if let Some(e) = self.engine.read().await.as_ref() {
            e.shutdown();
        }
        // 任务会在检测到 shutdown flag 后自行退出
        // 这里只是等待一下让它完成
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_vpn_config_default() {
        let config = VpnConfig::default();
        assert_eq!(config.heartbeat_interval_secs, 30);
    }
}
