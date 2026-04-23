//! 路由表管理模块
//!
//! 负责管理路由表和 DNS 配置

use anyhow::{Context, Result};
use ipnetwork::Ipv4Network;
use std::collections::HashMap;
use std::path::PathBuf;

/// 路由规则
#[derive(Debug, Clone)]
pub struct Route {
    /// 目标网络 (CIDR)
    pub dest: Ipv4Network,
    /// 网关 IP
    pub gateway: String,
    /// 接口名
    pub interface: String,
    /// 度量值
    pub metric: u32,
}

/// DNS 服务器配置
#[derive(Debug, Clone)]
pub struct DnsConfig {
    /// DNS 服务器列表
    pub servers: Vec<String>,
    /// 搜索域
    pub search_domains: Vec<String>,
}

/// DNS 劫持状态
#[derive(Debug, Clone)]
pub struct DnsHijackState {
    /// 是否启用
    pub enabled: bool,
    /// 原始 DNS 配置备份
    pub original_resolv_conf: Option<String>,
    /// VPN DNS 服务器
    pub vpn_dns: Vec<String>,
}

/// 路由管理器
pub struct RoutingTable {
    routes: HashMap<String, Route>,
    default_route: Option<Route>,
    dns_config: DnsConfig,
    dns_hijack: DnsHijackState,
}

impl RoutingTable {
    pub fn new() -> Self {
        Self {
            routes: HashMap::new(),
            default_route: None,
            dns_config: DnsConfig {
                servers: vec![],
                search_domains: vec![],
            },
            dns_hijack: DnsHijackState {
                enabled: false,
                original_resolv_conf: None,
                vpn_dns: vec!["10.8.0.1".to_string()],
            },
        }
    }

    /// 添加路由
    pub async fn add_route(&mut self, dest: &str, gateway: &str) -> Result<()> {
        let network: Ipv4Network = dest.parse()?;
        let route = Route {
            dest: network,
            gateway: gateway.to_string(),
            interface: "planet0".to_string(),
            metric: 100,
        };

        if dest == "0.0.0.0/0" {
            self.default_route = Some(route);
            tracing::info!("设置默认路由 via {}", gateway);
        } else {
            self.routes.insert(dest.to_string(), route);
            tracing::info!("添加路由 {} via {}", dest, gateway);
        }
        Ok(())
    }

    /// 删除路由
    pub async fn remove_route(&mut self, dest: &str) -> Result<()> {
        if dest == "0.0.0.0/0" {
            self.default_route = None;
            tracing::info!("删除默认路由");
        } else {
            self.routes.remove(dest);
            tracing::info!("删除路由 {}", dest);
        }
        Ok(())
    }

    /// 清除所有路由
    pub async fn clear(&mut self) -> Result<()> {
        self.routes.clear();
        self.default_route = None;
        tracing::info!("清除所有路由");
        Ok(())
    }

    /// 获取路由列表
    pub fn list(&self) -> Vec<&Route> {
        self.routes.values().collect()
    }

    /// 获取默认路由
    pub fn get_default_route(&self) -> Option<&Route> {
        self.default_route.as_ref()
    }

    /// 设置 DNS 配置
    pub fn set_dns(&mut self, servers: Vec<String>, search_domains: Vec<String>) {
        self.dns_config.servers = servers;
        self.dns_config.search_domains = search_domains;
        tracing::info!("DNS 配置已更新: {:?}", self.dns_config.servers);
    }

    /// 获取 DNS 配置
    pub fn get_dns(&self) -> &DnsConfig {
        &self.dns_config
    }

    /// 启用 DNS 劫持 (Linux)
    #[cfg(target_os = "linux")]
    pub async fn enable_dns_hijack(&mut self) -> Result<()> {
        if self.dns_hijack.enabled {
            tracing::info!("DNS 劫持已经启用");
            return Ok(());
        }

        // 备份原始 /etc/resolv.conf
        let resolv_path = PathBuf::from("/etc/resolv.conf");
        if resolv_path.exists() {
            let original = std::fs::read_to_string(&resolv_path)
                .context("读取原始 resolv.conf 失败")?;
            self.dns_hijack.original_resolv_conf = Some(original);
            tracing::debug!("已备份原始 resolv.conf");
        }

        // 写入新的 DNS 配置
        let mut new_conf = String::new();
        for dns in &self.dns_hijack.vpn_dns {
            new_conf.push_str(&format!("nameserver {}\n", dns));
        }
        for domain in &self.dns_config.search_domains {
            new_conf.push_str(&format!("search {}\n", domain));
        }

        // 尝试写入 /etc/resolv.conf
        // 注意: 在容器或无权限环境下可能失败
        match std::fs::write(&resolv_path, &new_conf) {
            Ok(_) => {
                self.dns_hijack.enabled = true;
                tracing::info!("DNS 劫持已启用: {:?}", self.dns_hijack.vpn_dns);
            }
            Err(e) => {
                tracing::warn!("无法写入 /etc/resolv.conf (需要 root 权限): {}", e);
                // 创建用户级 DNS 配置
                self.setup_user_dns().await?;
            }
        }

        Ok(())
    }

    /// 禁用 DNS 劫持 (Linux)
    #[cfg(target_os = "linux")]
    pub async fn disable_dns_hijack(&mut self) -> Result<()> {
        if !self.dns_hijack.enabled {
            tracing::info!("DNS 劫持已经禁用");
            return Ok(());
        }

        // 恢复原始 /etc/resolv.conf
        if let Some(original) = &self.dns_hijack.original_resolv_conf {
            std::fs::write("/etc/resolv.conf", original)
                .context("恢复原始 resolv.conf 失败")?;
        }

        self.dns_hijack.enabled = false;
        tracing::info!("DNS 劫持已禁用");
        Ok(())
    }

    /// 用户级 DNS 配置 (用于无 root 权限情况)
    #[cfg(target_os = "linux")]
    async fn setup_user_dns(&self) -> Result<()> {
        // 在用户目录创建 resolv.conf
        let home = dirs::home_dir().context("获取 home 目录失败")?;
        let etc_dir = home.join(".config").join("planet").join("etc");
        std::fs::create_dir_all(&etc_dir)?;

        let resolv_conf = etc_dir.join("resolv.conf");
        let mut content = String::new();
        for dns in &self.dns_hijack.vpn_dns {
            content.push_str(&format!("nameserver {}\n", dns));
        }
        std::fs::write(&resolv_conf, content)?;

        tracing::info!("已创建用户级 DNS 配置: {}", resolv_conf.display());
        Ok(())
    }

    /// 启用 DNS 劫持 (Windows)
    #[cfg(target_os = "windows")]
    pub async fn enable_dns_hijack(&mut self) -> Result<()> {
        if self.dns_hijack.enabled {
            return Ok(());
        }

        // 使用 netsh 设置 DNS
        for dns in &self.dns_hijack.vpn_dns {
            let output = std::process::Command::new("netsh")
                .args([
                    "interface", "ipv4", "add", "dnsserver",
                    "planet0", dns
                ])
                .output()
                .context("设置 DNS 服务器失败")?;

            if !output.status.success() {
                let stderr = String::from_utf8_lossy(&output.stderr);
                tracing::warn!("设置 DNS 失败 (非致命): {}", stderr);
            }
        }

        self.dns_hijack.enabled = true;
        tracing::info!("DNS 劫持已启用 (Windows)");
        Ok(())
    }

    /// 禁用 DNS 劫持 (Windows)
    #[cfg(target_os = "windows")]
    pub async fn disable_dns_hijack(&mut self) -> Result<()> {
        if !self.dns_hijack.enabled {
            return Ok(());
        }

        // 删除 DNS 服务器设置
        let output = std::process::Command::new("netsh")
            .args([
                "interface", "ipv4", "set", "dnsserver",
                "planet0", "dhcp"
            ])
            .output()
            .context("重置 DNS 服务器失败")?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            tracing::warn!("重置 DNS 失败: {}", stderr);
        }

        self.dns_hijack.enabled = false;
        tracing::info!("DNS 劫持已禁用 (Windows)");
        Ok(())
    }

    /// 启用 DNS 劫持 (macOS)
    #[cfg(target_os = "macos")]
    pub async fn enable_dns_hijack(&mut self) -> Result<()> {
        if self.dns_hijack.enabled {
            return Ok(());
        }

        // 使用 scutil 设置 DNS
        for dns in &self.dns_hijack.vpn_dns {
            let output = std::process::Command::new(" networksetup")
                .args([
                    "-setdnsservers", "Wi-Fi", dns
                ])
                .output()
                .context("设置 DNS 服务器失败")?;

            if !output.status.success() {
                let stderr = String::from_utf8_lossy(&output.stderr);
                tracing::warn!("设置 DNS 失败: {}", stderr);
            }
        }

        self.dns_hijack.enabled = true;
        tracing::info!("DNS 劫持已启用 (macOS)");
        Ok(())
    }

    /// 禁用 DNS 劫持 (macOS)
    #[cfg(target_os = "macos")]
    pub async fn disable_dns_hijack(&mut self) -> Result<()> {
        if !self.dns_hijack.enabled {
            return Ok(());
        }

        let output = std::process::Command::new("networksetup")
            .args([
                "-setdnsservers", "Wi-Fi", "Empty"
            ])
            .output()
            .context("重置 DNS 服务器失败")?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            tracing::warn!("重置 DNS 失败: {}", stderr);
        }

        self.dns_hijack.enabled = false;
        tracing::info!("DNS 劫持已禁用 (macOS)");
        Ok(())
    }

    /// 获取 DNS 劫持状态
    pub fn is_dns_hijack_enabled(&self) -> bool {
        self.dns_hijack.enabled
    }
}

impl Default for RoutingTable {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_route_creation() {
        let network: Ipv4Network = "10.8.0.0/24".parse().unwrap();
        let route = Route {
            dest: network,
            gateway: "10.8.0.1".to_string(),
            interface: "planet0".to_string(),
            metric: 100,
        };

        assert_eq!(route.gateway, "10.8.0.1");
        assert_eq!(route.interface, "planet0");
        assert_eq!(route.metric, 100);
    }

    #[test]
    fn test_dns_config() {
        let config = DnsConfig {
            servers: vec!["8.8.8.8".to_string(), "1.1.1.1".to_string()],
            search_domains: vec!["local".to_string()],
        };

        assert_eq!(config.servers.len(), 2);
        assert_eq!(config.search_domains.len(), 1);
    }

    #[test]
    fn test_routing_table_new() {
        let table = RoutingTable::new();
        assert!(table.list().is_empty());
        assert!(table.default_route.is_none());
        assert!(!table.is_dns_hijack_enabled());
    }

    #[test]
    fn test_routing_table_dns() {
        let table = RoutingTable::new();
        let dns = table.get_dns();
        // DNS should be empty initially (vpn_dns is internal state)
        assert!(dns.servers.is_empty());
    }

    #[tokio::test]
    async fn test_add_default_route() {
        let mut table = RoutingTable::new();

        // Set default route
        table.add_route("0.0.0.0/0", "10.8.0.1").await.unwrap();

        // Check default route
        assert!(table.default_route.is_some());
        let default = table.default_route.unwrap();
        assert_eq!(default.gateway, "10.8.0.1");
    }

    #[test]
    fn test_ipv4_network_parsing() {
        let network: Ipv4Network = "192.168.1.0/24".parse().unwrap();
        assert_eq!(network.prefix(), 24);
        assert_eq!(network.ip().to_string(), "192.168.1.0");
    }

    #[test]
    fn test_ipv4_network_invalid() {
        let result: Result<Ipv4Network, _> = "invalid".parse();
        assert!(result.is_err());
    }
}
