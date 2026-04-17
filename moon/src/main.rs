//! 闪连 VPN Moon 节点
//!
//! Moon 节点是流量中继服务器，负责：
//! - UDP/TCP/WSS 流量中继
//! - Moon 注册和心跳
//! - SLNX 协议处理
//! - ChaCha20 加密

use clap::Parser;
use serde::{Deserialize, Serialize};
use tracing_subscriber::{layer::SubscriberExt, util::SubscriberInitExt};

mod config;
mod relay;
mod tunnel;
mod signaling;
mod health;
mod auth;
mod stats;

use crate::auth::{generate_nonce, sha256_hex, HmacAuth};

/// Moon 注册请求
#[derive(Debug, Serialize)]
struct MoonRegisterRequest {
    domain: String,
    public_ip: String,
    gateway_ip: String,
    udp_port: u16,
    relay_port: u16,
    wss_port: u16,
    version: String,
}

/// Moon 注册响应
#[derive(Debug, Deserialize, Default)]
struct MoonRegisterResponse {
    id: String,
    api_key: String,
    #[serde(default)]
    api_secret: String,
    #[serde(default)]
    gateway_ip: Option<String>,
}

/// Moon 心跳请求
#[derive(Debug, Serialize)]
struct MoonHeartbeatRequest {
    domain: String,
    status: String,
    connections: u32,
}

/// Moon 心跳响应
#[derive(Debug, Deserialize, Default)]
struct MoonHeartbeatResponse {
    status: String,
    moon_count: u32,
    shutdown: bool,
    #[serde(default)]
    next_key: Option<String>,
}

/// 通用 API 响应
#[derive(Debug, Deserialize, Default)]
struct ApiResponse<T> {
    success: bool,
    #[serde(default)]
    message: Option<String>,
    #[serde(default)]
    data: Option<T>,
    #[serde(default)]
    error: Option<String>,
}

use config::Config;
use health::HealthState;

#[derive(Parser, Debug)]
#[command(name = "moon")]
#[command(about = "闪连 VPN Moon 节点 - 流量中继服务器")]
struct Args {
    /// Controller 服务器地址
    #[arg(long, default_value = "https://controller.ytw.fr:8443")]
    controller_url: String,

    /// Moon 域名
    #[arg(long)]
    domain: String,

    /// 公网 IP (留空则自动检测)
    #[arg(long)]
    public_ip: Option<String>,

    /// 网关 IP (VPN 内部网络网关)
    #[arg(long, default_value = "10.0.0.1")]
    gateway_ip: String,

    /// 中继端口 (TCP)
    #[arg(long, default_value = "11002")]
    relay_port: u16,

    /// UDP 端口 (STUN)
    #[arg(long, default_value = "11001")]
    udp_port: u16,

    /// WSS 中继端口
    #[arg(long, default_value = "11443")]
    wss_port: u16,

    /// 健康检查端口
    #[arg(long, default_value = "11003")]
    health_port: u16,

    /// API Key (首次注册后由 Controller 下发)
    #[arg(long)]
    api_key: Option<String>,

    /// API Secret
    #[arg(long)]
    api_secret: Option<String>,

    /// 日志级别
    #[arg(long, default_value = "info")]
    log_level: String,

    /// 以服务模式运行 (适合 systemd)
    #[arg(long)]
    run_as_service: bool,

    /// 跳过注册步骤（仅用于本地开发测试）
    #[arg(long)]
    skip_register: bool,
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let args = Args::parse();

    // 初始化日志
    tracing_subscriber::registry()
        .with(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| args.log_level.clone().into()),
        )
        .with(tracing_subscriber::fmt::layer())
        .init();

    tracing::info!("闪连 VPN Moon 节点启动中...");
    tracing::info!("版本: {}", env!("CARGO_PKG_VERSION"));
    tracing::info!("域名: {}", args.domain);
    tracing::info!("网关IP: {}", args.gateway_ip);
    tracing::info!("Controller: {}", args.controller_url);

    // 构建配置
    let config = Config {
        controller_url: args.controller_url.clone(),
        domain: args.domain.clone(),
        public_ip: args.public_ip.unwrap_or_default(),
        gateway_ip: args.gateway_ip.clone(),
        relay_port: args.relay_port,
        udp_port: args.udp_port,
        wss_port: args.wss_port,
        health_port: args.health_port,
        api_key: args.api_key.clone(),
        api_secret: args.api_secret.clone(),
        skip_register: args.skip_register,
    };

    // 启动 Moon 节点
    let moon = MoonNode::new(config);
    moon.run().await?;

    Ok(())
}

/// Moon 节点主结构
struct MoonNode {
    config: Config,
}

impl MoonNode {
    fn new(config: Config) -> Self {
        Self { config }
    }

    async fn run(&self) -> anyhow::Result<()> {
        // 注册到 Controller (除非 skip_register)
        let (api_key, api_secret) = if !self.config.skip_register {
            match self.register().await {
                Ok(creds) => (Some(creds.0), Some(creds.1)),
                Err(e) => {
                    tracing::error!("Moon 注册失败: {}", e);
                    return Err(e);
                }
            }
        } else {
            tracing::warn!("跳过注册步骤（仅用于开发测试）");
            (None, None)
        };

        // 更新配置中的凭证（用于心跳）
        let mut config = self.config.clone();
        config.api_key = api_key;
        config.api_secret = api_secret;

        // 启动各个端口监听
        let relay_config = config.clone();
        let relay_handle = tokio::spawn(async move {
            relay::start_tcp_relay(relay_config.relay_port).await
        });

        let udp_config = config.clone();
        let udp_handle = tokio::spawn(async move {
            relay::start_udp_stun(udp_config.udp_port).await
        });

        let wss_config = config.clone();
        let wss_handle = tokio::spawn(async move {
            relay::start_wss_relay(wss_config.wss_port).await
        });

        let health_config = config.clone();
        let health_handle = tokio::spawn(async move {
            let state = HealthState::new(env!("CARGO_PKG_VERSION").to_string());
            health::start_health_server(health_config.health_port, std::sync::Arc::new(state)).await
        });

        // 启动心跳定时器 (不等待其完成，它会一直运行)
        let heartbeat_config = config.clone();
        tokio::spawn(async move {
            loop {
                tokio::time::sleep(tokio::time::Duration::from_secs(30)).await;
                if let Err(e) = Self::send_heartbeat(&heartbeat_config).await {
                    tracing::error!("心跳发送失败: {}", e);
                }
            }
        });

        tracing::info!("Moon 节点已启动，监听端口: TCP {}, UDP {}, WSS {}, Health {}",
            config.relay_port, config.udp_port, config.wss_port, config.health_port);

        // 等待所有任务
        tokio::select! {
            result = relay_handle => { result??; }
            result = udp_handle => { result??; }
            result = wss_handle => { result??; }
            result = health_handle => { result??; }
        }

        Ok(())
    }

    async fn register(&self) -> anyhow::Result<(String, String)> {
        tracing::info!("正在注册到 Controller: {}", self.config.controller_url);

        // 获取公网 IP
        let public_ip = self.detect_public_ip().await?;
        tracing::info!("检测到公网 IP: {}", public_ip);

        // 构建注册请求
        let req = MoonRegisterRequest {
            domain: self.config.domain.clone(),
            public_ip: public_ip.clone(),
            gateway_ip: self.config.gateway_ip.clone(),
            udp_port: self.config.udp_port,
            relay_port: self.config.relay_port,
            wss_port: self.config.wss_port,
            version: env!("CARGO_PKG_VERSION").to_string(),
        };

        let client = reqwest::Client::new();
        let url = format!("{}/api/v1/moon/register", self.config.controller_url);

        tracing::debug!("发送注册请求到: {}", url);

        let resp = client
            .post(&url)
            .json(&req)
            .send()
            .await?;

        if !resp.status().is_success() {
            let status = resp.status();
            let body = resp.text().await.unwrap_or_default();
            tracing::error!("注册失败: {} - {}", status, body);
            anyhow::bail!("注册失败: {} - {}", status, body);
        }

        let api_resp: ApiResponse<MoonRegisterResponse> = resp.json().await?;

        if !api_resp.success {
            tracing::error!("注册失败: {:?}", api_resp.error);
            anyhow::bail!("注册失败: {:?}", api_resp.error);
        }

        let data = api_resp.data.ok_or_else(|| anyhow::anyhow!("注册响应数据为空"))?;

        tracing::info!("注册成功! Moon ID: {}", data.id);
        tracing::info!("获取到 API Key: {}", data.api_key);

        // 如果 Controller 返回了 gateway_ip，使用它
        if let Some(gateway_ip) = data.gateway_ip {
            tracing::info!("Controller 分配的 Gateway IP: {}", gateway_ip);
        }

        // 保存凭证到文件供下次使用
        let api_key = data.api_key.clone();
        let api_secret = data.api_secret.clone();
        self.save_credentials(&api_key, &api_secret)?;

        tracing::info!("Moon 注册完成");
        Ok((api_key, api_secret))
    }

    /// 检测公网 IP
    async fn detect_public_ip(&self) -> anyhow::Result<String> {
        // 如果配置了公网 IP，直接使用
        if !self.config.public_ip.is_empty() {
            return Ok(self.config.public_ip.clone());
        }

        // 尝试通过 STUN 获取公网 IP
        if let Ok(ip) = self.detect_ip_via_stun().await {
            return Ok(ip);
        }

        // 回退：使用本地 IP
        Ok("127.0.0.1".to_string())
    }

    /// 通过 STUN 检测公网 IP
    async fn detect_ip_via_stun(&self) -> anyhow::Result<String> {
        use std::net::UdpSocket;

        // 连接公共 STUN 服务器
        let stun_servers = ["1.1.1.1:3478", "8.8.8.8:3478", "stun.l.google.com:19302"];

        for server in &stun_servers {
            if let Ok(ip) = self.query_stun(server).await {
                return Ok(ip);
            }
        }

        anyhow::bail!("无法通过 STUN 检测公网 IP")
    }

    /// 查询 STUN 服务器获取公网 IP
    async fn query_stun(&self, server: &str) -> anyhow::Result<String> {
        use std::net::UdpSocket;

        let socket = UdpSocket::bind("0.0.0.0:0")?;
        socket.set_read_timeout(Some(std::time::Duration::from_secs(3)))?;

        // STUN Binding Request
        let mut request = vec![0u8; 20];
        request[0..2].copy_from_slice(&0x0001u16.to_be_bytes()); // Binding Request
        request[2..4].copy_from_slice(&0u16.to_be_bytes()); // Length = 0
        request[4..8].copy_from_slice(&0x2112A442u32.to_be_bytes()); // Magic Cookie
        let tid = uuid::Uuid::new_v4();
        request[8..20].copy_from_slice(&tid.as_bytes()[0..12]);

        socket.send_to(&request, server)?;

        let mut response = vec![0u8; 1024];
        let (len, _) = socket.recv_from(&mut response)?;

        if len < 20 {
            anyhow::bail!("STUN 响应太短");
        }

        // 解析 XOR-MAPPED-ADDRESS
        let mut offset = 20;
        while offset + 4 < len {
            let attr_type = u16::from_be_bytes([response[offset], response[offset + 1]]);
            let attr_len = u16::from_be_bytes([response[offset + 2], response[offset + 3]]) as usize;

            if attr_type == 0x0020 && attr_len == 8 {
                let port_offset = offset + 6;
                let port = u16::from_be_bytes([response[port_offset], response[port_offset + 1]])
                    ^ (0x2112A442u32 >> 16) as u16;

                let ip_offset = port_offset + 2;
                let ip_bytes = [
                    response[ip_offset] ^ 0x21,
                    response[ip_offset + 1] ^ 0x12,
                    response[ip_offset + 2] ^ 0xA4,
                    response[ip_offset + 3] ^ 0x42,
                ];
                let ip = format!("{}.{}.{}.{}", ip_bytes[0], ip_bytes[1], ip_bytes[2], ip_bytes[3]);
                return Ok(ip);
            }

            offset += 4 + attr_len;
            offset = (offset + 3) & !3;
        }

        anyhow::bail!("未找到 XOR-MAPPED-ADDRESS");
    }

    /// 保存凭证到文件
    fn save_credentials(&self, api_key: &str, api_secret: &str) -> anyhow::Result<()> {
        let cred_file = format!("{}/.moon_credentials", std::env::var("HOME").unwrap_or_default());
        let content = format!("{}\n{}", api_key, api_secret);
        std::fs::write(&cred_file, content)?;
        tracing::debug!("凭证已保存到: {}", cred_file);
        Ok(())
    }

    /// 加载凭证文件
    fn load_credentials() -> Option<(String, String)> {
        let cred_file = format!("{}/.moon_credentials", std::env::var("HOME").unwrap_or_default());
        if let Ok(content) = std::fs::read_to_string(&cred_file) {
            let lines: Vec<&str> = content.lines().collect();
            if lines.len() >= 2 {
                return Some((lines[0].to_string(), lines[1].to_string()));
            }
        }
        None
    }

    async fn send_heartbeat(config: &Config) -> anyhow::Result<()> {
        let api_key = match &config.api_key {
            Some(key) => key.clone(),
            None => {
                tracing::debug!("未配置 API Key，跳过心跳");
                return Ok(());
            }
        };
        let api_secret = match &config.api_secret {
            Some(secret) => secret.clone(),
            None => {
                tracing::debug!("未配置 API Secret，跳过心跳");
                return Ok(());
            }
        };

        let req = MoonHeartbeatRequest {
            domain: config.domain.clone(),
            status: "online".to_string(),
            connections: 0, // TODO: 获取当前连接数
        };

        let client = reqwest::Client::new();
        let url = format!("{}/api/v1/moon/heartbeat", config.controller_url);

        let body = serde_json::to_vec(&req)?;
        let body_hash = sha256_hex(&body);

        let timestamp = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_secs();

        let nonce = generate_nonce();

        let auth = HmacAuth::new(api_key.clone(), api_secret);
        let signature = auth.sign("POST", "/api/v1/moon/heartbeat", timestamp, &nonce, &body_hash);

        let auth_header = format!("Moon {} {} {} {}", api_key, timestamp, nonce, signature);

        let resp = client
            .post(&url)
            .header("Authorization", auth_header)
            .header("Content-Type", "application/json")
            .body(body)
            .send()
            .await?;

        if !resp.status().is_success() {
            let status = resp.status();
            let body = resp.text().await.unwrap_or_default();
            tracing::warn!("心跳失败: {} - {}", status, body);
            return Ok(());
        }

        let api_resp: ApiResponse<MoonHeartbeatResponse> = match resp.json().await {
            Ok(r) => r,
            Err(e) => {
                tracing::debug!("心跳响应解析失败: {}", e);
                return Ok(());
            }
        };

        if let Some(data) = api_resp.data {
            if data.shutdown {
                tracing::warn!("收到 Controller 关闭指令!");
                // TODO: 优雅关闭
            }
            tracing::debug!("心跳成功, MoonCount: {}", data.moon_count);
        }

        Ok(())
    }
}
