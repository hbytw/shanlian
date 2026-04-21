//! SDK 配置模块

use std::collections::HashMap;

/// SDK 配置
#[derive(Debug, Clone)]
pub struct SdkConfig {
    /// Controller 服务器地址
    pub controller_url: String,
    /// API Key
    pub api_key: Option<String>,
    /// API Secret
    pub api_secret: Option<String>,
    /// 设备 ID
    pub device_id: String,
    /// 设备名称
    pub device_name: Option<String>,
    /// 私钥路径
    pub identity_path: Option<String>,
    /// 心跳间隔（秒）
    pub heartbeat_interval: u64,
    /// 连接超时（秒）
    pub connection_timeout: u64,
}

impl Default for SdkConfig {
    fn default() -> Self {
        Self {
            controller_url: String::from("https://controller.shanlian.io"),
            api_key: None,
            api_secret: None,
            device_id: String::new(),
            device_name: None,
            identity_path: None,
            heartbeat_interval: 30,
            connection_timeout: 30,
        }
    }
}

/// 节点信息
#[derive(Debug, Clone)]
pub struct NodeInfo {
    pub moon_id: String,
    pub domain: String,
    pub relay_port: u16,
    pub wss_port: u16,
    pub weight: u16,
    pub online: bool,
}

/// 路由策略
#[derive(Debug, Clone)]
pub struct RoutePolicy {
    pub dest: String,
    pub gateway: String,
    pub metric: u32,
}

/// DNS 策略
#[derive(Debug, Clone)]
pub struct DnsPolicy {
    pub servers: Vec<String>,
    pub search_domains: Vec<String>,
    pub blocked_domains: Vec<String>,
}

/// URL 过滤规则
#[derive(Debug, Clone)]
pub struct UrlFilterRule {
    pub pattern: String,
    pub action: UrlFilterAction,
}

#[derive(Debug, Clone, Copy)]
pub enum UrlFilterAction {
    Allow,
    Block,
}

/// 流量限制策略
#[derive(Debug, Clone)]
pub struct TrafficLimitPolicy {
    pub quota_bytes: u64,
    pub period_seconds: u64,
}

/// 策略列表
#[derive(Debug, Clone)]
pub struct PolicyList {
    pub routes: Vec<RoutePolicy>,
    pub dns: DnsPolicy,
    pub url_filters: Vec<UrlFilterRule>,
    pub traffic_limit: Option<TrafficLimitPolicy>,
}
