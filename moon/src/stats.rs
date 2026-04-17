//! 流量统计上报
//!
//! 负责：
//! - Moon 节点流量统计
//! - 定时上报到 Controller

use std::sync::Arc;
use std::time::{Duration, Instant};

use tokio::sync::RwLock;
use tokio::time::interval;

/// 流量统计
#[derive(Debug, Clone, Default)]
pub struct TrafficStats {
    pub bytes_sent: u64,
    pub bytes_recv: u64,
    pub packets_sent: u64,
    pub packets_recv: u64,
}

impl TrafficStats {
    pub fn add_sent(&mut self, bytes: u64) {
        self.bytes_sent += bytes;
        self.packets_sent += 1;
    }

    pub fn add_recv(&mut self, bytes: u64) {
        self.bytes_recv += bytes;
        self.packets_recv += 1;
    }

    pub fn merge(&mut self, other: &TrafficStats) {
        self.bytes_sent += other.bytes_sent;
        self.bytes_recv += other.bytes_recv;
        self.packets_sent += other.packets_sent;
        self.packets_recv += other.packets_recv;
    }
}

/// 单个会话的流量统计
#[derive(Debug)]
pub struct SessionTraffic {
    pub session_id: [u8; 8],
    pub stats: TrafficStats,
    pub start_time: Instant,
    pub last_update: Instant,
}

impl SessionTraffic {
    pub fn new(session_id: [u8; 8]) -> Self {
        let now = Instant::now();
        Self {
            session_id,
            stats: Default::default(),
            start_time: now,
            last_update: now,
        }
    }

    pub fn add_sent(&mut self, bytes: u64) {
        self.stats.add_sent(bytes);
        self.last_update = Instant::now();
    }

    pub fn add_recv(&mut self, bytes: u64) {
        self.stats.add_recv(bytes);
        self.last_update = Instant::now();
    }
}

/// 统计收集器
pub struct StatsCollector {
    sessions: Arc<RwLock<HashMap<[u8; 8], SessionTraffic>>>,
    total: Arc<RwLock<TrafficStats>>,
    report_interval: Duration,
    controller_url: String,
    api_key: String,
}

impl StatsCollector {
    pub fn new(controller_url: String, api_key: String, report_interval: Duration) -> Self {
        Self {
            sessions: Arc::new(RwLock::new(HashMap::new())),
            total: Arc::new(RwLock::new(Default::default())),
            report_interval,
            controller_url,
            api_key,
        }
    }

    /// 记录发送字节
    pub async fn record_sent(&self, session_id: Option<[u8; 8]>, bytes: u64) {
        if let Some(id) = session_id {
            let mut sessions = self.sessions.write().await;
            if let Some(session) = sessions.get_mut(&id) {
                session.add_sent(bytes);
            }
        }
        self.total.write().await.add_sent(bytes);
    }

    /// 记录接收字节
    pub async fn record_recv(&self, session_id: Option<[u8; 8]>, bytes: u64) {
        if let Some(id) = session_id {
            let mut sessions = self.sessions.write().await;
            if let Some(session) = sessions.get_mut(&id) {
                session.add_recv(bytes);
            }
        }
        self.total.write().await.add_recv(bytes);
    }

    /// 获取总体统计
    pub async fn get_total(&self) -> TrafficStats {
        self.total.read().await.clone()
    }

    /// 获取所有会话统计
    pub async fn get_session_stats(&self) -> Vec<(String, TrafficStats)> {
        let sessions = self.sessions.read().await;
        sessions
            .iter()
            .map(|(id, s)| (hex::encode(id), s.stats.clone()))
            .collect()
    }

    /// 创建新会话统计
    pub async fn create_session(&self, session_id: [u8; 8]) {
        let mut sessions = self.sessions.write().await;
        sessions.insert(session_id, SessionTraffic::new(session_id));
    }

    /// 移除会话统计
    pub async fn remove_session(&self, session_id: &[u8; 8]) {
        let mut sessions = self.sessions.write().await;
        sessions.remove(session_id);
    }

    /// 重置统计
    pub async fn reset(&self) {
        let mut total = self.total.write().await;
        *total = Default::default();
        let mut sessions = self.sessions.write().await;
        sessions.clear();
    }

    /// 获取会话数
    pub async fn session_count(&self) -> usize {
        self.sessions.read().await.len()
    }
}

use std::collections::HashMap;

/// 启动定时上报任务
pub async fn start_stats_reporter(
    collector: Arc<StatsCollector>,
    controller_url: String,
    api_key: String,
    api_secret: String,
) {
    let mut ticker = interval(Duration::from_secs(60));

    loop {
        ticker.tick().await;
        let stats = collector.get_total().await;
        let sessions = collector.get_session_stats().await;

        tracing::debug!(
            "流量统计: sent={} bytes, recv={} bytes, sessions={}",
            stats.bytes_sent,
            stats.bytes_recv,
            sessions.len()
        );

        // 上报到 Controller
        if let Err(e) = report_stats(&controller_url, &api_key, &api_secret, &stats, &sessions).await {
            tracing::warn!("上报流量统计失败: {}", e);
        }
    }
}

async fn report_stats(
    controller_url: &str,
    api_key: &str,
    api_secret: &str,
    stats: &TrafficStats,
    sessions: &[(String, TrafficStats)],
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    use serde_json::json;

    let payload = json!({
        "api_key": api_key,
        "stats": {
            "bytes_sent": stats.bytes_sent,
            "bytes_recv": stats.bytes_recv,
            "packets_sent": stats.packets_sent,
            "packets_recv": stats.packets_recv,
        },
        "sessions": sessions.iter().map(|(id, s)| {
            json!({
                "session_id": id,
                "bytes_sent": s.bytes_sent,
                "bytes_recv": s.bytes_recv,
            })
        }).collect::<Vec<_>>(),
    });

    let timestamp = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs();

    let body = serde_json::to_string(&payload)?;
    let body_hash = sha256_hex(body.as_bytes());

    // 计算 HMAC 签名
    let signature = {
        use hmac::Mac;
        type HmacSha256 = hmac::Hmac<sha2::Sha256>;
        let mut mac = HmacSha256::new_from_slice(api_secret.as_bytes())?;
        mac.update(format!("POST/api/v1/moon/stats{}{}", timestamp, body_hash).as_bytes());
        hex::encode(mac.finalize().into_bytes())
    };

    let url = format!("{}/api/v1/moon/stats", controller_url);

    // 每次创建新的 Client（reqwest::Client 不是 Send/Sync）
    let client = reqwest::Client::new();
    let response = client
        .post(&url)
        .header("Content-Type", "application/json")
        .header("X-API-Key", api_key)
        .header("X-Timestamp", timestamp.to_string())
        .header("X-Nonce", uuid::Uuid::new_v4().to_string())
        .header("X-Signature", &signature)
        .body(body)
        .send()
        .await?;

    if !response.status().is_success() {
        return Err(format!("上报失败: {}", response.status()).into());
    }

    Ok(())
}

fn sha256_hex(data: &[u8]) -> String {
    use sha2::Digest;
    hex::encode(Sha256::digest(data))
}

use sha2::Sha256;

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn test_traffic_stats() {
        let collector = StatsCollector::new(
            "http://localhost:8443".to_string(),
            "test_key".to_string(),
            Duration::from_secs(60),
        );

        collector.record_sent(None, 100).await;
        collector.record_recv(None, 50).await;

        let total = collector.get_total().await;
        assert_eq!(total.bytes_sent, 100);
        assert_eq!(total.bytes_recv, 50);
    }

    #[tokio::test]
    async fn test_session_stats() {
        let collector = StatsCollector::new(
            "http://localhost:8443".to_string(),
            "test_key".to_string(),
            Duration::from_secs(60),
        );

        let session_id = [1, 2, 3, 4, 5, 6, 7, 8];
        collector.create_session(session_id).await;
        collector.record_sent(Some(session_id), 200).await;

        let sessions = collector.get_session_stats().await;
        assert_eq!(sessions.len(), 1);
        assert_eq!(sessions[0].1.bytes_sent, 200);
    }
}
