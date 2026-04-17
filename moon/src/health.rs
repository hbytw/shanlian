//! 健康检查服务
//!
//! 负责：
//! - TCP 11003 端口 HTTP 健康检查
//! - Moon 节点状态上报

use axum::{
    extract::State,
    http::StatusCode,
    response::Json,
    routing::get,
    Router,
};
use serde::{Deserialize, Serialize};
use std::sync::Arc;
use std::time::SystemTime;
use tokio::sync::RwLock;

/// Moon 健康状态
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HealthStatus {
    pub status: String,
    pub uptime_seconds: u64,
    pub version: String,
    pub relay_connections: usize,
    pub tunnel_sessions: usize,
    pub memory_mb: u64,
    pub timestamp: u64,
}

/// 健康检查共享状态
pub struct HealthState {
    pub start_time: SystemTime,
    pub relay_connections: Arc<RwLock<usize>>,
    pub tunnel_sessions: Arc<RwLock<usize>>,
    pub version: String,
}

impl HealthState {
    pub fn new(version: String) -> Self {
        Self {
            start_time: SystemTime::now(),
            relay_connections: Arc::new(RwLock::new(0)),
            tunnel_sessions: Arc::new(RwLock::new(0)),
            version,
        }
    }

    pub async fn get_status(&self) -> HealthStatus {
        let uptime = SystemTime::now()
            .duration_since(self.start_time)
            .unwrap_or_default()
            .as_secs();

        let relay_connections = *self.relay_connections.read().await;
        let tunnel_sessions = *self.tunnel_sessions.read().await;

        // 获取内存使用 (Linux)
        let memory_mb = get_memory_usage();

        HealthStatus {
            status: "healthy".to_string(),
            uptime_seconds: uptime,
            version: self.version.clone(),
            relay_connections,
            tunnel_sessions,
            memory_mb,
            timestamp: SystemTime::now()
                .duration_since(SystemTime::UNIX_EPOCH)
                .unwrap_or_default()
                .as_secs(),
        }
    }

    pub async fn increment_relay_connections(&self) {
        *self.relay_connections.write().await += 1;
    }

    pub async fn decrement_relay_connections(&self) {
        *self.relay_connections.write().await -= 1;
    }

    pub async fn increment_tunnel_sessions(&self) {
        *self.tunnel_sessions.write().await += 1;
    }

    pub async fn decrement_tunnel_sessions(&self) {
        *self.tunnel_sessions.write().await -= 1;
    }
}

/// 获取进程内存使用 (MB)
fn get_memory_usage() -> u64 {
    #[cfg(target_os = "linux")]
    {
        let pid = std::process::id();
        let stat_path = format!("/proc/{}/status", pid);
        if let Ok(content) = std::fs::read_to_string(stat_path) {
            for line in content.lines() {
                if line.starts_with("VmRSS:") {
                    let parts: Vec<&str> = line.split_whitespace().collect();
                    if parts.len() >= 2 {
                        if let Ok(kb) = parts[1].parse::<u64>() {
                            return kb / 1024; // KB to MB
                        }
                    }
                }
            }
        }
    }
    0
}

/// 健康检查处理器
async fn health_handler(
    State(state): State<Arc<HealthState>>,
) -> (StatusCode, Json<HealthStatus>) {
    let status = state.get_status().await;
    (StatusCode::OK, Json(status))
}

/// 启动健康检查服务器
pub async fn start_health_server(port: u16, state: Arc<HealthState>) -> anyhow::Result<()> {
    let app = Router::new()
        .route("/health", get(health_handler))
        .route("/metrics", get(metrics_handler))
        .with_state(state);

    let addr = format!("0.0.0.0:{}", port);
    tracing::info!("健康检查服务器启动，监听端口 {}", port);

    let listener = tokio::net::TcpListener::bind(&addr).await?;
    axum::serve(listener, app).await?;

    Ok(())
}

/// Prometheus 风格指标
async fn metrics_handler(
    State(state): State<Arc<HealthState>>,
) -> (StatusCode, String) {
    let status = state.get_status().await;

    let metrics = format!(
        "# HELP moon_uptime_seconds Moon node uptime in seconds\n\
         # TYPE moon_uptime_seconds gauge\n\
         moon_uptime_seconds {}\n\
         # HELP moon_relay_connections Number of relay connections\n\
         # TYPE moon_relay_connections gauge\n\
         moon_relay_connections {}\n\
         # HELP moon_tunnel_sessions Number of active tunnel sessions\n\
         # TYPE moon_tunnel_sessions gauge\n\
         moon_tunnel_sessions {}\n\
         # HELP moon_memory_mb Memory usage in MB\n\
         # TYPE moon_memory_mb gauge\n\
         moon_memory_mb {}\n",
        status.uptime_seconds,
        status.relay_connections,
        status.tunnel_sessions,
        status.memory_mb
    );

    (StatusCode::OK, metrics)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn test_health_status() {
        let state = HealthState::new("1.0.0".to_string());
        let status = state.get_status().await;

        assert_eq!(status.status, "healthy");
        assert_eq!(status.version, "1.0.0");
    }
}
