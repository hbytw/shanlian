//! 隧道管理模块

use std::time::{Duration, Instant};

/// 隧道信息
#[derive(Debug, Clone)]
pub struct Tunnel {
    pub tunnel_type: TunnelType,
    pub moon_id: String,
    pub local_ip: String,
    pub remote_ip: String,
    pub local_port: u16,
    pub remote_port: u16,
    pub bytes_sent: u64,
    pub bytes_recv: u64,
    pub rtt_ms: u32,
    pub loss_rate: f32,
    pub active: bool,
    pub established_at: Option<Instant>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TunnelType {
    Udp,
    Tcp,
    Wss,
    Moon,
}

impl Default for Tunnel {
    fn default() -> Self {
        Self {
            tunnel_type: TunnelType::Udp,
            moon_id: String::new(),
            local_ip: String::new(),
            remote_ip: String::new(),
            local_port: 0,
            remote_port: 0,
            bytes_sent: 0,
            bytes_recv: 0,
            rtt_ms: 0,
            loss_rate: 0.0,
            active: false,
            established_at: None,
        }
    }
}

impl Tunnel {
    pub fn new(tunnel_type: TunnelType, moon_id: &str) -> Self {
        Self {
            tunnel_type,
            moon_id: moon_id.to_string(),
            ..Default::default()
        }
    }

    pub fn connected_time(&self) -> Duration {
        self.established_at
            .map(|t| t.elapsed())
            .unwrap_or_default()
    }
}

/// 隧道统计
#[derive(Debug, Clone, Default)]
pub struct TunnelStats {
    pub bytes_sent: u64,
    pub bytes_recv: u64,
    pub packets_sent: u64,
    pub packets_recv: u64,
    pub rtt_ms: u32,
    pub loss_rate: f32,
}
