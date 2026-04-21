//! 连接相关命令

use serde::{Deserialize, Serialize};
use std::fs;
use std::path::PathBuf;
use std::sync::Mutex;
use std::time::Instant;

/// 获取配置文件目录
fn get_config_dir() -> PathBuf {
    dirs::config_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join("planet-gui")
}

/// 获取或创建持久化的设备 ID
fn get_or_create_device_id() -> String {
    let config_path = get_config_dir().join("device_id");
    if let Ok(id) = fs::read_to_string(&config_path) {
        let id = id.trim().to_string();
        if !id.is_empty() {
            tracing::info!("使用已存在的设备 ID: {}", id);
            return id;
        }
    }
    // 生成新的设备 ID 并保存
    let new_id = uuid::Uuid::new_v4().to_string();
    if let Err(e) = fs::create_dir_all(get_config_dir()) {
        tracing::warn!("创建设备配置目录失败: {}", e);
    } else if let Err(e) = fs::write(&config_path, &new_id) {
        tracing::warn!("保存设备 ID 失败: {}", e);
    }
    tracing::info!("生成新设备 ID: {}", new_id);
    new_id
}

/// 获取操作系统类型
fn get_os_type() -> String {
    #[cfg(target_os = "windows")]
    return "windows".to_string();
    #[cfg(target_os = "linux")]
    return "linux".to_string();
    #[cfg(target_os = "macos")]
    return "darwin".to_string();
    #[cfg(target_os = "android")]
    return "android".to_string();
    #[cfg(target_os = "ios")]
    return "ios".to_string();
    #[cfg(not(any(target_os = "windows", target_os = "linux", target_os = "macos", target_os = "android", target_os = "ios")))]
    return "unknown".to_string();
}

/// 获取操作系统版本
fn get_os_version() -> String {
    #[cfg(target_os = "windows")]
    return std::env::consts::ARCH.to_string();
    #[cfg(target_os = "linux")]
    {
        if let Ok(parts) = fs::read_to_string("/etc/os-release") {
            for line in parts.lines() {
                if line.starts_with("PRETTY_NAME=") {
                    return line.trim_start_matches("PRETTY_NAME=").trim_matches('"').to_string();
                }
            }
        }
        return "Linux".to_string();
    }
    #[cfg(target_os = "macos")]
    return "macOS".to_string();
    #[cfg(not(any(target_os = "windows", target_os = "linux", target_os = "macos")))]
    return "unknown".to_string();
}

/// 获取公网 IP（通过外部服务）
async fn get_public_ip() -> Option<String> {
    // 尝试多个 IP 检测服务
    let services = [
        "https://api.ipify.org",
        "https://icanhazip.com",
        "https://checkip.amazonaws.com",
    ];

    let client = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(5))
        .build()
        .ok()?;

    for service in &services {
        if let Ok(response) = client.get(*service).send().await {
            if let Ok(ip) = response.text().await {
                let ip = ip.trim().to_string();
                if !ip.is_empty() && ip.parse::<std::net::Ipv4Addr>().is_ok() {
                    tracing::info!("获取到公网 IP: {}", ip);
                    return Some(ip);
                }
            }
        }
    }
    tracing::warn!("获取公网 IP 失败");
    None
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ConnectionStatus {
    pub connected: bool,
    pub server: String,
    pub ip: String,
    pub uptime_secs: u64,
    pub last_error: Option<String>,
}

impl Default for ConnectionStatus {
    fn default() -> Self {
        Self {
            connected: false,
            server: String::new(),
            ip: String::new(),
            uptime_secs: 0,
            last_error: None,
        }
    }
}

/// 连接状态（带锁）
struct SharedStatus {
    status: ConnectionStatus,
    connected_at: Option<Instant>,
}

impl Default for SharedStatus {
    fn default() -> Self {
        Self {
            status: ConnectionStatus::default(),
            connected_at: None,
        }
    }
}

/// 连接状态（全局）
static CONNECTION_STATUS: std::sync::LazyLock<Mutex<SharedStatus>> =
    std::sync::LazyLock::new(|| Mutex::new(SharedStatus::default()));

/// 获取当前已运行时间（秒）
fn get_uptime_secs(connected_at: Option<Instant>) -> u64 {
    connected_at.map(|t| t.elapsed().as_secs()).unwrap_or(0)
}

/// 生成设备签名
fn generate_signature(device_id: &str, public_key: &str) -> String {
    use std::collections::hash_map::DefaultHasher;
    use std::hash::{Hash, Hasher};

    let mut hasher = DefaultHasher::new();
    format!("{}_{}", device_id, public_key).hash(&mut hasher);
    format!("{:x}", hasher.finish())
}

/// 发送到 Controller API
async fn register_to_controller(server: &str, device_id: &str) -> Result<RegisterResponse, String> {
    // 构建请求 URL
    let url = format!("{}/api/v1/peer/register", server.trim_end_matches('/'));
    tracing::info!("正在请求: {}", url);

    // 获取主机名
    let hostname = hostname::get()
        .map(|h| h.to_string_lossy().to_string())
        .unwrap_or_else(|_| "unknown".to_string());

    // 获取公网 IP
    let public_ip = get_public_ip().await;

    // 获取操作系统信息
    let os_type = get_os_type();
    let os_version = get_os_version();
    let client_version = env!("CARGO_PKG_VERSION").to_string();

    // 构建请求体
    // device_id: 持久化的设备 ID
    // network_id: 默认网络的 UUID
    let request = PeerRegisterRequest {
        device_id: device_id.to_string(),
        network_id: "00000000-0000-0000-0000-000000000001".to_string(), // 默认网络
        public_ip,
        public_port: None, // TODO: 通过 ICE/STUN 获取
        nat_type: Some("unknown".to_string()), // TODO: 通过 NAT 检测获取
        hostname,
        os_type: os_type.clone(),
        os_version: os_version.clone(),
        client_version: client_version.clone(),
        tunnel_type: Some("moon".to_string()),
    };

    let client = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(30))
        .build()
        .map_err(|e| format!("创建 HTTP 客户端失败: {}", e))?;

    let response = client
        .post(&url)
        .json(&request)
        .send()
        .await
        .map_err(|e| {
            tracing::error!("请求错误详情: {}", e);
            if e.is_connect() {
                format!("连接服务器失败，请检查服务器地址是否正确: {}", e)
            } else if e.is_timeout() {
                format!("连接超时: {}", e)
            } else {
                format!("发送注册请求失败: {}", e)
            }
        })?;

    if !response.status().is_success() {
        let status = response.status();
        let body = response.text().await.unwrap_or_default();
        return Err(format!("注册失败 ({}): {}", status, body));
    }

    response
        .json::<RegisterResponse>()
        .await
        .map_err(|e| format!("解析注册响应失败: {}", e))
}

#[derive(Serialize)]
struct PeerRegisterRequest {
    device_id: String,
    network_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    public_ip: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    public_port: Option<u16>,
    #[serde(skip_serializing_if = "Option::is_none")]
    nat_type: Option<String>,
    hostname: String,
    os_type: String,
    os_version: String,
    client_version: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    tunnel_type: Option<String>,
}

#[derive(Deserialize)]
struct RegisterResponse {
    #[serde(rename = "data")]
    data: RegisterData,
}

#[derive(Deserialize)]
struct RegisterData {
    #[serde(rename = "peer_id")]
    peer_id: String,
    #[serde(rename = "assigned_ip")]
    assigned_ip: String,
    #[serde(rename = "network_info")]
    network_info: Option<NetworkInfo>,
    #[serde(rename = "moon_list")]
    moon_list: Option<Vec<MoonInfo>>,
}

#[derive(Deserialize)]
struct NetworkInfo {
    id: String,
    name: String,
    cidr: String,
    gateway_ip: String,
}

#[derive(Deserialize)]
struct MoonInfo {
    id: String,
    domain: String,
    public_ip: String,
    relay_port: u16,
    wss_port: u16,
    weight: u32,
    online: bool,
}

/// 连接到 VPN
#[tauri::command]
pub async fn connect(server: String) -> Result<ConnectionStatus, String> {
    tracing::info!("连接命令: {}", server);

    // 验证服务器地址
    if server.is_empty() {
        return Err("服务器地址不能为空".to_string());
    }

    // 解析服务器地址确保格式正确
    let server_url = if server.starts_with("http://") || server.starts_with("https://") {
        server.clone()
    } else {
        format!("https://{}", server)
    };

    // 获取持久化的设备 ID
    let device_id = get_or_create_device_id();
    tracing::info!("使用设备 ID: {}", device_id);

    // 发送到 Controller 进行注册
    match register_to_controller(&server_url, &device_id).await {
        Ok(response) => {
            tracing::info!("注册成功! peer_id={}, assigned_ip={}",
                response.data.peer_id, response.data.assigned_ip);

            let mut shared = CONNECTION_STATUS.lock().map_err(|e| format!("锁错误: {}", e))?;
            shared.status.connected = true;
            shared.status.server = server_url;
            shared.status.ip = response.data.assigned_ip;
            shared.status.uptime_secs = 0;
            shared.status.last_error = None;
            shared.connected_at = Some(Instant::now());

            tracing::info!("连接成功");
            Ok(shared.status.clone())
        }
        Err(e) => {
            tracing::error!("注册失败: {}", e);

            let mut shared = CONNECTION_STATUS.lock().map_err(|e| format!("锁错误: {}", e))?;
            shared.status.connected = false;
            shared.status.last_error = Some(e.clone());

            Err(e)
        }
    }
}

/// 断开 VPN 连接
#[tauri::command]
pub async fn disconnect() -> Result<(), String> {
    tracing::info!("断开连接命令");

    let mut shared = CONNECTION_STATUS.lock().map_err(|e| format!("锁错误: {}", e))?;
    shared.status.connected = false;
    shared.status.server.clear();
    shared.status.ip.clear();
    shared.status.uptime_secs = 0;
    shared.connected_at = None;

    tracing::info!("已断开连接");
    Ok(())
}

/// 获取连接状态
#[tauri::command]
pub async fn get_status() -> Result<ConnectionStatus, String> {
    let shared = CONNECTION_STATUS.lock().map_err(|e| format!("锁错误: {}", e))?;
    let mut status = shared.status.clone();
    status.uptime_secs = get_uptime_secs(shared.connected_at);
    Ok(status)
}
