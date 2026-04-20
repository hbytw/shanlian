//! 日志上报模块
//!
//! 负责审计日志的本地存储和上报

use anyhow::Result;
use std::collections::VecDeque;
use std::sync::Arc;
use std::time::{Duration, SystemTime};
use tokio::sync::{mpsc, RwLock};
use tokio::time;

/// 日志级别
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum LogLevel {
    Debug,
    Info,
    Warning,
    Error,
}

impl std::fmt::Display for LogLevel {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            LogLevel::Debug => write!(f, "DEBUG"),
            LogLevel::Info => write!(f, "INFO"),
            LogLevel::Warning => write!(f, "WARN"),
            LogLevel::Error => write!(f, "ERROR"),
        }
    }
}

/// 审计日志类型
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AuditType {
    /// 访问日志
    Access,
    /// 策略变更
    PolicyChange,
    /// 隧道日志
    Tunnel,
    /// 上下线日志
    Online,
    /// 错误日志
    Error,
}

impl std::fmt::Display for AuditType {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            AuditType::Access => write!(f, "访问"),
            AuditType::PolicyChange => write!(f, "策略变更"),
            AuditType::Tunnel => write!(f, "隧道"),
            AuditType::Online => write!(f, "上下线"),
            AuditType::Error => write!(f, "错误"),
        }
    }
}

/// 审计日志条目
#[derive(Debug, Clone)]
pub struct AuditLog {
    /// 时间戳
    pub timestamp: SystemTime,
    /// 日志级别
    pub level: LogLevel,
    /// 日志类型
    pub audit_type: AuditType,
    /// 设备 ID
    pub device_id: String,
    /// 用户 ID
    pub user_id: Option<String>,
    /// URL
    pub url: Option<String>,
    /// HTTP 方法
    pub method: Option<String>,
    /// 状态码
    pub status_code: Option<u16>,
    /// 接收字节数
    pub bytes_in: u64,
    /// 发送字节数
    pub bytes_out: u64,
    /// 延迟 (毫秒)
    pub duration_ms: u64,
    /// 源 IP
    pub src_ip: Option<String>,
    /// 目标 IP
    pub dest_ip: Option<String>,
    /// 额外信息 (JSON 格式)
    pub extra: Option<String>,
    /// 错误信息
    pub error: Option<String>,
}

impl AuditLog {
    /// 创建访问日志
    pub fn access(
        device_id: &str,
        url: &str,
        method: &str,
        status_code: u16,
        bytes_in: u64,
        bytes_out: u64,
        duration_ms: u64,
    ) -> Self {
        Self {
            timestamp: SystemTime::now(),
            level: LogLevel::Info,
            audit_type: AuditType::Access,
            device_id: device_id.to_string(),
            user_id: None,
            url: Some(url.to_string()),
            method: Some(method.to_string()),
            status_code: Some(status_code),
            bytes_in,
            bytes_out,
            duration_ms,
            src_ip: None,
            dest_ip: None,
            extra: None,
            error: None,
        }
    }

    /// 创建隧道日志
    pub fn tunnel(device_id: &str, action: &str, tunnel_id: &str, info: &str) -> Self {
        Self {
            timestamp: SystemTime::now(),
            level: LogLevel::Info,
            audit_type: AuditType::Tunnel,
            device_id: device_id.to_string(),
            user_id: None,
            url: None,
            method: None,
            status_code: None,
            bytes_in: 0,
            bytes_out: 0,
            duration_ms: 0,
            src_ip: None,
            dest_ip: None,
            extra: Some(serde_json::json!({
                "action": action,
                "tunnel_id": tunnel_id,
                "info": info
            }).to_string()),
            error: None,
        }
    }

    /// 创建上下线日志
    pub fn online(device_id: &str, online: bool, reason: Option<&str>) -> Self {
        Self {
            timestamp: SystemTime::now(),
            level: LogLevel::Info,
            audit_type: AuditType::Online,
            device_id: device_id.to_string(),
            user_id: None,
            url: None,
            method: None,
            status_code: None,
            bytes_in: 0,
            bytes_out: 0,
            duration_ms: 0,
            src_ip: None,
            dest_ip: None,
            extra: Some(serde_json::json!({
                "online": online,
                "reason": reason
            }).to_string()),
            error: None,
        }
    }

    /// 创建错误日志
    pub fn error(device_id: &str, error: &str, context: Option<&str>) -> Self {
        Self {
            timestamp: SystemTime::now(),
            level: LogLevel::Error,
            audit_type: AuditType::Error,
            device_id: device_id.to_string(),
            user_id: None,
            url: None,
            method: None,
            status_code: None,
            bytes_in: 0,
            bytes_out: 0,
            duration_ms: 0,
            src_ip: None,
            dest_ip: None,
            extra: context.map(|c| serde_json::json!({"context": c}).to_string()),
            error: Some(error.to_string()),
        }
    }

    /// 转换为 JSON 格式
    pub fn to_json(&self) -> Result<String> {
        Ok(serde_json::to_string(self)?)
    }
}

/// 审计客户端配置
#[derive(Debug, Clone)]
pub struct AuditConfig {
    /// 上报服务器地址
    pub server_url: String,
    /// API Key
    pub api_key: String,
    /// 批量上报大小
    pub batch_size: usize,
    /// 本地缓冲大小
    pub buffer_size: usize,
    /// 上报间隔 (秒)
    pub report_interval_secs: u64,
    /// 最小上报间隔 (秒)
    pub min_report_interval_secs: u64,
    /// 是否启用本地存储
    pub local_storage_enabled: bool,
    /// 本地存储路径
    pub local_storage_path: Option<String>,
}

impl Default for AuditConfig {
    fn default() -> Self {
        Self {
            server_url: "https://audit.ytw.fr/api/v1/audit/log".to_string(),
            api_key: String::new(),
            batch_size: 100,
            buffer_size: 1000,
            report_interval_secs: 60,
            min_report_interval_secs: 10,
            local_storage_enabled: true,
            local_storage_path: None,
        }
    }
}

/// 审计日志上报器
pub struct AuditReporter {
    /// 配置
    config: AuditConfig,
    /// 日志缓冲
    buffer: VecDeque<AuditLog>,
    /// HTTP 客户端
    client: reqwest::Client,
    /// 设备 ID
    device_id: String,
    /// 发送通道
    sender: mpsc::Sender<AuditLog>,
    /// 运行状态
    running: Arc<RwLock<bool>>,
}

impl AuditReporter {
    /// 创建审计上报器
    pub async fn new(device_id: &str, config: AuditConfig) -> Result<Self> {
        let (sender, mut receiver) = mpsc::channel::<AuditLog>(config.buffer_size);

        let reporter = Self {
            config: config.clone(),
            buffer: VecDeque::with_capacity(config.buffer_size),
            client: reqwest::Client::builder()
                .timeout(Duration::from_secs(30))
                .build()
                .context("创建 HTTP 客户端失败")?,
            device_id: device_id.to_string(),
            sender,
            running: Arc::new(RwLock::new(false)),
        };

        // 启动后台处理任务
        let running = reporter.running.clone();
        let device_id = reporter.device_id.clone();
        let client = reporter.client.clone();
        let config = reporter.config.clone();

        *reporter.running.write().await = true;

        // 启动日志处理任务
        tokio::spawn(async move {
            let mut interval = time::interval(Duration::from_secs(config.report_interval_secs));

            loop {
                tokio::select! {
                    Some(log) = receiver.recv() => {
                        tracing::trace!("收到审计日志: {:?}", log.audit_type);
                    }
                    _ = interval.tick() => {
                        // 定时上报
                    }
                }

                if !*running.read().await {
                    break;
                }
            }
        });

        tracing::info!("审计日志上报器初始化完成");
        Ok(reporter)
    }

    /// 获取发送器
    pub fn sender(&self) -> mpsc::Sender<AuditLog> {
        self.sender.clone()
    }

    /// 记录访问日志
    pub async fn log_access(&self, log: AuditLog) -> Result<()> {
        self.add_log(log).await
    }

    /// 记录隧道日志
    pub async fn log_tunnel(&self, log: AuditLog) -> Result<()> {
        self.add_log(log).await
    }

    /// 记录上下线日志
    pub async fn log_online(&self, log: AuditLog) -> Result<()> {
        self.add_log(log).await
    }

    /// 记录错误日志
    pub async fn log_error(&self, log: AuditLog) -> Result<()> {
        self.add_log(log).await
    }

    /// 添加日志到缓冲
    async fn add_log(&self, log: AuditLog) -> Result<()> {
        let mut buffer = self.buffer.write().await;

        // 如果缓冲已满，移除最旧的日志
        if buffer.len() >= self.config.buffer_size {
            tracing::warn!("审计日志缓冲已满，丢弃最旧的日志");
            buffer.pop_front();
        }

        buffer.push_back(log);
        Ok(())
    }

    /// 获取缓冲中的日志数量
    pub async fn buffer_len(&self) -> usize {
        self.buffer.read().await.len()
    }

    /// 批量上报日志
    pub async fn flush(&self) -> Result<()> {
        let logs = {
            let mut buffer = self.buffer.write().await
                ;
            if buffer.is_empty() {
                return Ok(());
            }

            // 获取待上报的日志
            let logs: Vec<_> = buffer
                .drain(..self.config.batch_size.min(buffer.len()))
                .collect();
            logs
        };

        if logs.is_empty() {
            return Ok(());
        }

        tracing::info!("开始上报 {} 条审计日志", logs.len());

        // 构建请求
        let url = &self.config.server_url;
        let body = serde_json::json!({
            "device_id": self.device_id,
            "logs": logs
        });

        let response = self.client
            .post(url)
            .header("X-Api-Key", &self.config.api_key)
            .header("Content-Type", "application/json")
            .json(&body)
            .send()
            .await
            .context("发送审计日志失败")?;

        if response.status().is_success() {
            tracing::info!("审计日志上报成功: {} 条", logs.len());
        } else {
            let status = response.status();
            let error_text = response.text().await.unwrap_or_default();
            tracing::error!("审计日志上报失败 ({}): {}", status, error_text);

            // 重新放回缓冲
            let mut buffer = self.buffer.write().await;
            for log in logs {
                buffer.push_front(log);
            }
        }

        Ok(())
    }

    /// 停止上报器
    pub async fn stop(&self) -> Result<()> {
        *self.running.write().await = false;

        // 最后一次上报
        self.flush().await?;

        tracing::info!("审计日志上报器已停止");
        Ok(())
    }
}

/// 简化审计日志 (用于频繁记录的访问日志)
#[derive(Debug)]
pub struct AccessLogEntry {
    pub url: String,
    pub method: String,
    pub status_code: u16,
    pub bytes_in: u64,
    pub bytes_out: u64,
    pub duration_ms: u64,
    pub timestamp: SystemTime,
}

impl AccessLogEntry {
    pub fn to_audit_log(&self, device_id: &str) -> AuditLog {
        AuditLog::access(
            device_id,
            &self.url,
            &self.method,
            self.status_code,
            self.bytes_in,
            self.bytes_out,
            self.duration_ms,
        )
    }
}

/// 审计统计
#[derive(Debug, Default)]
pub struct AuditStats {
    pub total_logs: u64,
    pub access_logs: u64,
    pub tunnel_logs: u64,
    pub online_logs: u64,
    pub error_logs: u64,
    pub buffer_usage: usize,
}
