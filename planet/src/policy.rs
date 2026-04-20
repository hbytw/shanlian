//! 策略执行模块
//!
//! 负责路由策略、DNS 策略、URL 过滤和流量限制

use anyhow::Result;
use std::collections::HashMap;
use std::net::Ipv4Addr;
use std::sync::Arc;
use tokio::sync::RwLock;

/// 策略类型
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PolicyType {
    /// 路由策略
    Route,
    /// DNS 策略
    Dns,
    /// URL 过滤策略
    UrlFilter,
    /// 流量限制策略
    TrafficLimit,
}

impl std::fmt::Display for PolicyType {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            PolicyType::Route => write!(f, "路由策略"),
            PolicyType::Dns => write!(f, "DNS策略"),
            PolicyType::UrlFilter => write!(f, "URL过滤"),
            PolicyType::TrafficLimit => write!(f, "流量限制"),
        }
    }
}

/// 策略动作
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PolicyAction {
    /// 允许
    Allow,
    /// 拒绝
    Deny,
    /// 重定向
    Redirect,
    /// 路由到指定网关
    RouteVia,
}

impl std::fmt::Display for PolicyAction {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            PolicyAction::Allow => write!(f, "允许"),
            PolicyAction::Deny => write!(f, "拒绝"),
            PolicyAction::Redirect => write!(f, "重定向"),
            PolicyAction::RouteVia => write!(f, "路由"),
        }
    }
}

/// 策略规则
#[derive(Debug, Clone)]
pub struct PolicyRule {
    /// 策略 ID
    pub id: String,
    /// 策略类型
    pub policy_type: PolicyType,
    /// 策略名称
    pub name: String,
    /// 目标 (CIDR, 域名, URL 等)
    pub dest: Option<String>,
    /// 源 IP
    pub src_ip: Option<String>,
    /// 目标端口
    pub dest_port: Option<u16>,
    /// 协议
    pub protocol: Option<String>,
    /// 动作
    pub action: PolicyAction,
    /// 优先级 (数字越小优先级越高)
    pub priority: u32,
    /// 是否启用
    pub enabled: bool,
}

impl PolicyRule {
    /// 创建路由策略
    pub fn route(id: &str, name: &str, dest: &str, gateway: &str, priority: u32) -> Self {
        Self {
            id: id.to_string(),
            policy_type: PolicyType::Route,
            name: name.to_string(),
            dest: Some(dest.to_string()),
            src_ip: None,
            dest_port: None,
            protocol: None,
            action: PolicyAction::RouteVia,
            priority,
            enabled: true,
        }
    }

    /// 创建 DNS 策略
    pub fn dns(id: &str, name: &str, domain: &str, action: PolicyAction, priority: u32) -> Self {
        Self {
            id: id.to_string(),
            policy_type: PolicyType::Dns,
            name: name.to_string(),
            dest: Some(domain.to_string()),
            src_ip: None,
            dest_port: Some(53),
            protocol: Some("UDP".to_string()),
            action,
            priority,
            enabled: true,
        }
    }

    /// 创建 URL 过滤策略
    pub fn url_filter(id: &str, name: &str, url_pattern: &str, action: PolicyAction, priority: u32) -> Self {
        Self {
            id: id.to_string(),
            policy_type: PolicyType::UrlFilter,
            name: name.to_string(),
            dest: Some(url_pattern.to_string()),
            src_ip: None,
            dest_port: None,
            protocol: None,
            action,
            priority,
            enabled: true,
        }
    }

    /// 创建流量限制策略
    pub fn traffic_limit(id: &str, name: &str, dest: &str, limit_mbps: u32, priority: u32) -> Self {
        Self {
            id: id.to_string(),
            policy_type: PolicyType::TrafficLimit,
            name: name.to_string(),
            dest: Some(dest.to_string()),
            src_ip: None,
            dest_port: None,
            protocol: None,
            action: PolicyAction::Deny,
            priority,
            enabled: true,
        }
    }
}

/// 流量配额信息
#[derive(Debug)]
pub struct TrafficQuota {
    /// 策略 ID
    pub policy_id: String,
    /// 已使用字节数
    pub used_bytes: u64,
    /// 限制字节数 (0 = 无限制)
    pub limit_bytes: u64,
    /// 重置时间
    pub reset_at: std::time::Instant,
    /// 是否超限
    pub exceeded: bool,
}

impl TrafficQuota {
    pub fn new(policy_id: &str, limit_bytes: u64, duration_secs: u64) -> Self {
        Self {
            policy_id: policy_id.to_string(),
            used_bytes: 0,
            limit_bytes,
            reset_at: std::time::Instant::now() + std::time::Duration::from_secs(duration_secs),
            exceeded: false,
        }
    }

    /// 添加流量
    pub fn add_bytes(&mut self, bytes: u64) {
        self.used_bytes += bytes;
        if self.limit_bytes > 0 && self.used_bytes > self.limit_bytes {
            self.exceeded = true;
        }
    }

    /// 检查是否超限
    pub fn is_exceeded(&self) -> bool {
        self.exceeded || (self.limit_bytes > 0 && self.used_bytes > self.limit_bytes)
    }

    /// 重置配额
    pub fn reset(&mut self) {
        self.used_bytes = 0;
        self.exceeded = false;
        self.reset_at = std::time::Instant::now() + std::time::Duration::from_secs(3600);
    }
}

/// DNS 记录
#[derive(Debug, Clone)]
pub struct DnsRecord {
    pub domain: String,
    pub ip: String,
    pub ttl: u32,
}

/// 本地 DNS 缓存
pub struct DnsCache {
    records: HashMap<String, DnsRecord>,
    /// 域名黑名单 (恶意域名, 广告域名)
    blocked_domains: HashMap<String, bool>,
}

impl DnsCache {
    pub fn new() -> Self {
        Self {
            records: HashMap::new(),
            blocked_domains: HashMap::new(),
        }
    }

    /// 添加 DNS 记录
    pub fn add_record(&mut self, domain: &str, ip: &str, ttl: u32) {
        self.records.insert(
            domain.to_string(),
            DnsRecord {
                domain: domain.to_string(),
                ip: ip.to_string(),
                ttl,
            },
        );
    }

    /// 查找 DNS 记录
    pub fn lookup(&self, domain: &str) -> Option<&DnsRecord> {
        self.records.get(domain)
    }

    /// 添加域名到黑名单
    pub fn block_domain(&mut self, domain: &str) {
        self.blocked_domains.insert(domain.to_string(), true);
        tracing::info!("域名 {} 已加入黑名单", domain);
    }

    /// 从黑名单移除
    pub fn unblock_domain(&mut self, domain: &str) {
        self.blocked_domains.remove(domain);
        tracing::info!("域名 {} 已从黑名单移除", domain);
    }

    /// 检查域名是否被阻止
    pub fn is_blocked(&self, domain: &str) -> bool {
        // 检查精确匹配
        if self.blocked_domains.contains_key(domain) {
            return true;
        }

        // 检查通配符匹配
        let parts: Vec<&str> = domain.split('.').collect();
        for i in 0..parts.len() {
            let pattern = format!("*.{}", parts[i..].join("."));
            if self.blocked_domains.contains_key(&pattern) {
                return true;
            }
        }

        false
    }

    /// 清空缓存
    pub fn clear(&mut self) {
        self.records.clear();
    }
}

impl Default for DnsCache {
    fn default() -> Self {
        Self::new()
    }
}

/// URL 匹配器
pub struct UrlMatcher {
    /// 精确匹配的域名
    exact_domains: HashMap<String, bool>,
    /// 通配符匹配的域名
    wildcard_domains: HashMap<String, bool>,
    /// URL 正则模式
    url_patterns: Vec<regex::Regex>,
}

impl UrlMatcher {
    pub fn new() -> Self {
        Self {
            exact_domains: HashMap::new(),
            wildcard_domains: HashMap::new(),
            url_patterns: Vec::new(),
        }
    }

    /// 添加域名过滤
    pub fn add_domain(&mut self, domain: &str) -> Result<()> {
        if domain.starts_with("*.") {
            self.wildcard_domains.insert(domain[2..].to_string(), true);
        } else {
            self.exact_domains.insert(domain.to_string(), true);
        }
        Ok(())
    }

    /// 添加 URL 模式
    pub fn add_url_pattern(&mut self, pattern: &str) -> Result<()> {
        let regex = regex::Regex::new(pattern)
            .with_context(|| format!("无效的 URL 正则模式: {}", pattern))?;
        self.url_patterns.push(regex);
        Ok(())
    }

    /// 检查 URL 是否匹配
    pub fn matches(&self, url: &str) -> bool {
        // 解析 URL 获取域名
        if let Ok(parsed) = url::Url::parse(url) {
            let host = parsed.host_str().unwrap_or("");

            // 检查精确匹配
            if self.exact_domains.contains_key(host) {
                return true;
            }

            // 检查通配符匹配
            let parts: Vec<&str> = host.split('.').collect();
            for i in 0..parts.len() {
                let pattern = format!("*.{}", parts[i..].join("."));
                if self.wildcard_domains.contains_key(&pattern) {
                    return true;
                }
            }
        }

        // 检查 URL 正则模式
        for re in &self.url_patterns {
            if re.is_match(url) {
                return true;
            }
        }

        false
    }
}

impl Default for UrlMatcher {
    fn default() -> Self {
        Self::new()
    }
}

/// 策略引擎
pub struct PolicyEngine {
    /// 策略规则列表
    rules: Vec<PolicyRule>,
    /// 流量配额
    quotas: HashMap<String, TrafficQuota>,
    /// DNS 缓存
    dns_cache: DnsCache,
    /// URL 匹配器
    url_matcher: UrlMatcher,
    /// 是否启用
    enabled: Arc<RwLock<bool>>,
}

impl PolicyEngine {
    pub fn new() -> Self {
        Self {
            rules: Vec::new(),
            quotas: HashMap::new(),
            dns_cache: DnsCache::new(),
            url_matcher: UrlMatcher::new(),
            enabled: Arc::new(RwLock::new(true)),
        }
    }

    /// 添加策略规则
    pub fn add_rule(&mut self, rule: PolicyRule) {
        self.rules.push(rule);
        self.rules.sort_by_key(|r| r.priority);
        tracing::info!("添加策略规则: {}", rule.name);
    }

    /// 移除策略规则
    pub fn remove_rule(&mut self, id: &str) -> Result<()> {
        let pos = self.rules.iter().position(|r| r.id == id);
        if let Some(pos) = pos {
            let rule = self.rules.remove(pos);
            tracing::info!("移除策略规则: {}", rule.name);
            Ok(())
        } else {
            anyhow::bail!("策略规则 {} 不存在", id);
        }
    }

    /// 获取所有策略
    pub fn list_rules(&self) -> Vec<&PolicyRule> {
        self.rules.iter().collect()
    }

    /// 根据类型获取策略
    pub fn get_rules_by_type(&self, policy_type: PolicyType) -> Vec<&PolicyRule> {
        self.rules
            .iter()
            .filter(|r| r.policy_type == policy_type)
            .collect()
    }

    /// 检查 IP 包是否允许通过 (路由决策)
    pub fn check_route(&self, dest_ip: &str) -> Option<PolicyAction> {
        for rule in &self.rules {
            if rule.policy_type == PolicyType::Route && rule.enabled {
                if let Some(ref dest) = rule.dest {
                    // 简单前缀匹配
                    if dest_ip.starts_with(&dest[..dest.len().min(dest.len())]) {
                        tracing::debug!("路由策略匹配: {} -> {:?}", dest_ip, rule.action);
                        return Some(rule.action);
                    }
                }
            }
        }
        None
    }

    /// 检查 DNS 请求
    pub fn check_dns(&self, domain: &str) -> (Option<PolicyAction>, Option<String>) {
        // 首先检查黑名单
        if self.dns_cache.is_blocked(domain) {
            tracing::debug!("DNS 请求被阻止: {}", domain);
            return (Some(PolicyAction::Deny), None);
        }

        // 检查 DNS 策略
        for rule in &self.rules {
            if rule.policy_type == PolicyType::Dns && rule.enabled {
                if let Some(ref dest) = rule.dest {
                    if domain.ends_with(dest) || domain == dest {
                        tracing::debug!("DNS 策略匹配: {} -> {:?}", domain, rule.action);
                        return (Some(rule.action), None);
                    }
                }
            }
        }

        (None, None)
    }

    /// 检查 URL 是否允许
    pub fn check_url(&self, url: &str) -> Option<PolicyAction> {
        if self.url_matcher.matches(url) {
            tracing::debug!("URL 过滤匹配: {}", url);
            return Some(PolicyAction::Deny);
        }

        for rule in &self.rules {
            if rule.policy_type == PolicyType::UrlFilter && rule.enabled {
                if let Some(ref dest) = rule.dest {
                    if url.contains(dest) {
                        tracing::debug!("URL 策略匹配: {} -> {:?}", url, rule.action);
                        return Some(rule.action);
                    }
                }
            }
        }

        None
    }

    /// 检查流量配额
    pub fn check_quota(&self, policy_id: &str) -> bool {
        if let Some(quota) = self.quotas.get(policy_id) {
            return !quota.is_exceeded();
        }
        true
    }

    /// 添加流量配额
    pub fn add_quota(&mut self, quota: TrafficQuota) {
        self.quotas.insert(quota.policy_id.clone(), quota);
    }

    /// 记录流量使用
    pub fn record_usage(&mut self, policy_id: &str, bytes: u64) {
        if let Some(quota) = self.quotas.get_mut(policy_id) {
            quota.add_bytes(bytes);
            if quota.is_exceeded() {
                tracing::warn!("流量配额 {} 已超限!", policy_id);
            }
        }
    }

    /// 添加广告域名
    pub fn add_blocked_domain(&mut self, domain: &str) {
        self.dns_cache.block_domain(domain);
    }

    /// 添加恶意域名列表
    pub fn add_malware_domains(&mut self, domains: &[&str]) {
        for domain in domains {
            self.dns_cache.block_domain(domain);
        }
    }

    /// 启用/禁用策略引擎
    pub async fn set_enabled(&self, enabled: bool) {
        *self.enabled.write().await = enabled;
    }

    /// 是否启用
    pub async fn is_enabled(&self) -> bool {
        *self.enabled.read().await
    }

    /// 清空所有策略
    pub fn clear(&mut self) {
        self.rules.clear();
        self.quotas.clear();
        self.dns_cache.clear();
        tracing::info!("策略引擎已清空");
    }
}

impl Default for PolicyEngine {
    fn default() -> Self {
        Self::new()
    }
}

/// 策略统计
#[derive(Debug)]
pub struct PolicyStats {
    pub total_rules: usize,
    pub active_rules: usize,
    pub blocked_domains: usize,
    pub exceeded_quotas: usize,
}

impl PolicyEngine {
    pub fn stats(&self) -> PolicyStats {
        PolicyStats {
            total_rules: self.rules.len(),
            active_rules: self.rules.iter().filter(|r| r.enabled).count(),
            blocked_domains: self.dns_cache.blocked_domains.len(),
            exceeded_quotas: self.quotas.values().filter(|q| q.exceeded).count(),
        }
    }
}
