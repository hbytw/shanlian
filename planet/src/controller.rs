//! Controller 通信模块
//!
//! 负责与 Controller 服务器通信，包括注册、心跳、节点发现等

use anyhow::{Context, Result};
use reqwest::{Client, Url};
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::RwLock;
use std::collections::HashMap;

use crate::crypto::{Crypto, KeyPair};

/// Controller API 端点
const API_PEER_REGISTER: &str = "/api/v1/peer/register";
const API_PEER_HEARTBEAT: &str = "/api/v1/peer/heartbeat";
const API_PEER_DISCOVER: &str = "/api/v1/peer/discover";
const API_VERSION: &str = "/api/v1/version/client";

/// API 响应包装器
#[derive(Debug, serde::Deserialize)]
pub struct ApiResponse<T> {
    pub message: String,
    pub data: T,
}

/// Peer 注册请求
#[derive(Debug, serde::Serialize)]
pub struct PeerRegisterRequest {
    pub device_id: String,
    pub network_id: String,
    pub public_key: String,
    pub signature: String,
    pub hostname: String,
    pub os: String,
    pub version: String,
}

/// Peer 注册响应
#[derive(Debug, serde::Deserialize)]
pub struct PeerRegisterResponse {
    pub peer_id: String,
    pub assigned_ip: String,
    pub network_info: Option<NetworkInfo>,
    pub policy_list: Option<Vec<PolicyRule>>,
    pub moon_list: Option<Vec<MoonInfo>>,
}

/// 网络信息
#[derive(Debug, serde::Deserialize)]
pub struct NetworkInfo {
    #[serde(alias = "id")]
    pub network_id: String,
    pub name: String,
    #[serde(alias = "cidr")]
    pub ip_range: String,
    pub gateway_ip: String,
    #[serde(default)]
    pub dns_servers: Option<Vec<String>>,
}

/// 策略规则
#[derive(Debug, serde::Deserialize)]
pub struct PolicyRule {
    pub policy_id: String,
    pub policy_type: String,
    pub dest: Option<String>,
    pub action: String,
}

/// Peer 心跳请求
#[derive(Debug, serde::Serialize)]
pub struct PeerHeartbeatRequest {
    pub device_id: String,
    pub tunnel_type: String,
    pub tunnel_quality: TunnelQuality,
}

/// 隧道质量信息
#[derive(Debug, serde::Serialize)]
pub struct TunnelQuality {
    pub rtt_ms: u32,
    pub loss_rate: f32,
    pub recv_bytes: u64,
    pub send_bytes: u64,
}

/// Peer 心跳响应
#[derive(Debug, serde::Deserialize)]
pub struct PeerHeartbeatResponse {
    pub status: String,
    pub policy_updates: Option<Vec<PolicyUpdate>>,
    pub moon_list: Option<Vec<MoonInfo>>,
}

/// 策略更新
#[derive(Debug, serde::Deserialize)]
pub struct PolicyUpdate {
    pub policy_id: String,
    pub action: String,
}

/// Moon 地址信息
#[derive(Debug, serde::Deserialize, Clone)]
pub struct MoonAddress {
    #[serde(rename = "type")]
    pub addr_type: String,
    pub host: String,
    pub port: u16,
}

/// Moon 信息
#[derive(Debug, serde:: Deserialize, Clone)]
pub struct MoonInfo {
    pub id: String,
    pub domain: String,
    #[serde(default)]
    pub public_ip: Option<String>,
    #[serde(default)]
    pub internal_ip: Option<String>,
    pub relay_port: u16,
    pub wss_port: u16,
    pub weight: u32,
    pub online: bool,
    #[serde(default)]
    pub addresses: Option<Vec<MoonAddress>>,
}

/// 发现响应
#[derive(Debug, serde::Deserialize)]
pub struct DiscoverResponse {
    pub peer_id: String,
    pub public_ip: String,
    pub port: u16,
    pub nat_type: String,
}

/// 版本信息响应
#[derive(Debug, serde::Deserialize)]
pub struct VersionResponse {
    pub version: String,
    pub min_version: String,
    pub download_url: Option<String>,
}

/// Controller 客户端
pub struct ControllerClient {
    /// HTTP 客户端
    client: Client,
    /// 服务器 URL
    server_url: String,
    /// 设备 ID
    device_id: String,
    /// API Key
    api_key: Option<String>,
    /// API Secret
    api_secret: Option<String>,
    /// 密钥对
    key_pair: Arc<KeyPair>,
    /// Nonce 计数器
    nonce_counter: Arc<RwLock<u64>>,
    /// 连接状态
    connected: Arc<RwLock<bool>>,
}

impl ControllerClient {
    /// 创建新的 Controller 客户端
    pub async fn new(server_url: &str, device_id: &str) -> Result<Self> {
        let client = Client::builder()
            .timeout(Duration::from_secs(30))
            .connect_timeout(Duration::from_secs(10))
            .build()
            .context("创建 HTTP 客户端失败")?;

        let key_pair = KeyPair::generate();

        Ok(Self {
            client,
            server_url: server_url.to_string(),
            device_id: device_id.to_string(),
            api_key: None,
            api_secret: None,
            key_pair: Arc::new(key_pair),
            nonce_counter: Arc::new(RwLock::new(0)),
            connected: Arc::new(RwLock::new(false)),
        })
    }

    /// 从文件加载密钥对
    pub fn load_key_pair(&mut self, path: &std::path::PathBuf) -> Result<()> {
        self.key_pair = Arc::new(KeyPair::load(path)?);
        Ok(())
    }

    /// 保存密钥对到文件
    pub fn save_key_pair(&self, path: &std::path::PathBuf) -> Result<()> {
        self.key_pair.save(path)
    }

    /// 获取设备 ID
    pub fn device_id(&self) -> &str {
        &self.device_id
    }

    /// 获取公钥
    pub fn public_key(&self) -> String {
        self.key_pair.public_key()
    }

    /// 检查是否已连接
    pub async fn is_connected(&self) -> bool {
        *self.connected.read().await
    }

    /// 设置连接状态
    async fn set_connected(&self, connected: bool) {
        *self.connected.write().await = connected;
    }

    /// 生成 HMAC 签名
    async fn sign_request(&self, method: &str, path: &str, body: &[u8]) -> Result<String> {
        let nonce = self.generate_nonce().await;
        let timestamp = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_secs();

        let signature_data = format!(
            "{}{}{}{}{}",
            nonce, timestamp, method, path, hex::encode(body)
        );

        let signature = self.key_pair.sign(signature_data.as_bytes());
        let signature_hex = hex::encode(signature.to_bytes());

        Ok(signature_hex)
    }

    /// 生成 Nonce
    async fn generate_nonce(&self) -> String {
        let mut counter = self.nonce_counter.write().await;
        *counter += 1;
        format!("{}_{}", self.device_id, *counter)
    }

    /// 构建带签名的请求
    async fn build_signed_request(
        &self,
        method: &str,
        path: &str,
        body: &[u8],
    ) -> Result<reqwest::Request> {
        let url = Url::parse(&format!("{}{}", self.server_url, path))?;
        let mut request = match method {
            "GET" => self.client.get(url),
            "POST" => self.client.post(url),
            _ => anyhow::bail!("不支持的 HTTP 方法: {}", method),
        };

        // 添加认证头
        let signature = self.sign_request(method, path, body).await?;
        let timestamp = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_secs();

        request = request
            .header("X-Device-ID", &self.device_id)
            .header("X-Nonce", self.generate_nonce().await.as_str())
            .header("X-Timestamp", timestamp.to_string())
            .header("X-Signature", &signature);

        if let Some(api_key) = &self.api_key {
            request = request.header("X-Api-Key", api_key);
        }

        if !body.is_empty() {
            request = request
                .header("Content-Type", "application/json")
                .body(body.to_vec());
        }

        request.build().map_err(|e| anyhow::anyhow!("构建请求失败: {}", e))
    }

    /// 发送请求并处理响应
    async fn send_request<T: serde::de::DeserializeOwned>(
        &self,
        method: &str,
        path: &str,
        body: &[u8],
    ) -> Result<T> {
        let request = self.build_signed_request(method, path, body).await?;

        let response = self.client
            .execute(request)
            .await
            .context("发送请求失败")?;

        let status = response.status();

        if status.is_success() {
            let body = response.bytes().await.context("读取响应体失败")?;
            let result = serde_json::from_slice(&body)
                .context("解析响应 JSON 失败")?;
            Ok(result)
        } else {
            let body = response.text().await.unwrap_or_default();
            anyhow::bail!("请求失败 ({}): {}", status, body)
        }
    }

    /// 注册设备到 Controller
    pub async fn register(&mut self) -> Result<PeerRegisterResponse> {
        tracing::info!("开始注册设备到 Controller: {}", self.device_id);

        let hostname = hostname::get()
            .map(|h| h.to_string_lossy().to_string())
            .unwrap_or_else(|_| "unknown".to_string());

        let os = std::env::consts::OS;

        // 生成设备签名
        let challenge = format!("register:{}", self.device_id);
        let signature = self.key_pair.sign(challenge.as_bytes());
        let signature_hex = hex::encode(signature.to_bytes());

        let request = PeerRegisterRequest {
            device_id: self.device_id.clone(),
            network_id: "00000000-0000-0000-0000-000000000001".to_string(), // 默认网络
            public_key: self.public_key(),
            signature: signature_hex,
            hostname,
            os: os.to_string(),
            version: env!("CARGO_PKG_VERSION").to_string(),
        };

        let body = serde_json::to_vec(&request)?;
        let wrapped: ApiResponse<PeerRegisterResponse> = self.send_request("POST", API_PEER_REGISTER, &body).await?;

        let response = wrapped.data;

        self.set_connected(true).await;
        tracing::info!("设备注册成功: peer_id={}", response.peer_id);

        Ok(response)
    }

    /// 发送心跳
    pub async fn heartbeat(
        &self,
        tunnel_type: &str,
        quality: TunnelQuality,
    ) -> Result<PeerHeartbeatResponse> {
        let request = PeerHeartbeatRequest {
            device_id: self.device_id.clone(),
            tunnel_type: tunnel_type.to_string(),
            tunnel_quality: quality,
        };

        let body = serde_json::to_vec(&request)?;
        let response: PeerHeartbeatResponse = self.send_request("POST", API_PEER_HEARTBEAT, &body).await?;

        tracing::debug!("心跳响应: status={}", response.status);
        Ok(response)
    }

    /// 发现节点地址
    pub async fn discover(&self, peer_id: &str) -> Result<DiscoverResponse> {
        let body = serde_json::to_vec(&serde_json::json!({ "peer_id": peer_id }))?;
        let response: DiscoverResponse = self.send_request("POST", API_PEER_DISCOVER, &body).await?;
        Ok(response)
    }

    /// 查询客户端版本
    pub async fn check_version(&self) -> Result<VersionResponse> {
        let body = serde_json::to_vec(&serde_json::json!({
            "os": std::env::consts::OS,
            "arch": std::env::consts::ARCH,
            "version": env!("CARGO_PKG_VERSION")
        }))?;
        let response: VersionResponse = self.send_request("POST", API_VERSION, &body).await?;
        Ok(response)
    }
}

/// 创建默认的 Controller 客户端
pub async fn create_controller_client(
    server_url: &str,
    device_id: &str,
) -> Result<ControllerClient> {
    let client = ControllerClient::new(server_url, device_id).await?;

    // 尝试加载已保存的密钥对
    let config_dir = dirs::config_local_dir()
        .unwrap_or_else(|| std::path::PathBuf::from("."))
        .join("shanlian")
        .join("planet");

    let key_path = config_dir.join("identity");
    if key_path.exists() {
        // 如果有保存的密钥，加载它
        tracing::info!("从文件加载密钥对: {}", key_path.display());
    }

    Ok(client)
}
