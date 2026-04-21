//! SDK 句柄管理模块

use std::sync::Arc;
use tokio::sync::mpsc;

/// SDK 句柄
pub struct SdkHandle {
    pub id: isize,
    pub sender: mpsc::Sender<SdkCommand>,
}

pub enum SdkCommand {
    Connect,
    Disconnect,
    SwitchTunnel(TunnelType),
    ConnectPeer(String),
    DisconnectPeer(String),
}

#[derive(Debug, Clone, Copy)]
pub enum TunnelType {
    Udp,
    Tcp,
    Wss,
    Moon,
}

impl From<i32> for TunnelType {
    fn from(v: i32) -> Self {
        match v {
            0 => TunnelType::Udp,
            1 => TunnelType::Tcp,
            2 => TunnelType::Wss,
            3 => TunnelType::Moon,
            _ => TunnelType::Udp,
        }
    }
}
