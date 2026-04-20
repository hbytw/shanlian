//! Session 管理模块

use std::collections::HashMap;
use std::time::{Duration, Instant};

/// Session 信息
#[derive(Debug)]
pub struct Session {
    /// Session ID
    pub id: String,
    /// 节点 ID
    pub peer_id: String,
    /// 分配的 IP
    pub assigned_ip: String,
    /// 网络 ID
    pub network_id: String,
    /// 创建时间
    pub created_at: Instant,
    /// 最后活动时间
    pub last_active: Instant,
    /// 隧道 ID
    pub tunnel_id: Option<String>,
    /// 接收字节数
    pub recv_bytes: u64,
    /// 发送字节数
    pub send_bytes: u64,
}

impl Session {
    /// 检查是否超时
    pub fn is_stale(&self, timeout: Duration) -> bool {
        self.last_active.elapsed() > timeout
    }

    /// 更新活动时间
    pub fn touch(&mut self) {
        self.last_active = Instant::now();
    }

    /// 添加接收字节
    pub fn add_recv_bytes(&mut self, bytes: u64) {
        self.recv_bytes += bytes;
        self.touch();
    }

    /// 添加发送字节
    pub fn add_send_bytes(&mut self, bytes: u64) {
        self.send_bytes += bytes;
        self.touch();
    }
}

/// Session 管理器
pub struct SessionManager {
    sessions: HashMap<String, Session>,
    /// 心跳超时时间
    heartbeat_timeout: Duration,
}

impl SessionManager {
    pub fn new() -> Self {
        Self {
            sessions: HashMap::new(),
            heartbeat_timeout: Duration::from_secs(90),
        }
    }

    /// 创建新 Session
    pub fn create(&mut self, peer_id: &str, assigned_ip: &str, network_id: &str) -> String {
        let id = uuid::Uuid::new_v4().to_string();
        let session = Session {
            id: id.clone(),
            peer_id: peer_id.to_string(),
            assigned_ip: assigned_ip.to_string(),
            network_id: network_id.to_string(),
            created_at: Instant::now(),
            last_active: Instant::now(),
            tunnel_id: None,
            recv_bytes: 0,
            send_bytes: 0,
        };
        self.sessions.insert(id.clone(), session);
        tracing::info!("创建 Session {} for peer {}", id, peer_id);
        id
    }

    /// 获取 Session
    pub fn get(&self, id: &str) -> Option<&Session> {
        self.sessions.get(id)
    }

    /// 获取 Session (可变)
    pub fn get_mut(&mut self, id: &str) -> Option<&mut Session> {
        self.sessions.get_mut(id)
    }

    /// 根据 peer_id 获取 Session
    pub fn get_by_peer(&self, peer_id: &str) -> Option<&Session> {
        self.sessions.values().find(|s| s.peer_id == peer_id)
    }

    /// 更新心跳
    pub fn touch(&mut self, id: &str) {
        if let Some(session) = self.sessions.get_mut(id) {
            session.touch();
        }
    }

    /// 关闭 Session
    pub fn close(&mut self, id: &str) {
        if let Some(session) = self.sessions.remove(id) {
            tracing::info!("关闭 Session {} for peer {}", id, session.peer_id);
        }
    }

    /// 清理超时会话
    pub fn cleanup_stale(&mut self) -> Vec<String> {
        let stale_ids: Vec<String> = self
            .sessions
            .iter()
            .filter(|(_, s)| s.is_stale(self.heartbeat_timeout))
            .map(|(id, _)| id.clone())
            .collect();

        for id in &stale_ids {
            if let Some(session) = self.sessions.remove(id) {
                tracing::warn!("Session {} 超时关闭 (peer {})", id, session.peer_id);
            }
        }
        stale_ids
    }

    /// 获取所有 Session
    pub fn list(&self) -> Vec<&Session> {
        self.sessions.values().collect()
    }

    /// 获取 Session 数量
    pub fn count(&self) -> usize {
        self.sessions.len()
    }
}

impl Default for SessionManager {
    fn default() -> Self {
        Self::new()
    }
}
