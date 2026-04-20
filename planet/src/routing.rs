//! 路由表管理模块

use anyhow::Result;
use ipnetwork::Ipv4Network;
use std::collections::HashMap;

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

/// 路由管理器
pub struct RoutingTable {
    routes: HashMap<String, Route>,
    default_route: Option<Route>,
}

impl RoutingTable {
    pub fn new() -> Self {
        Self {
            routes: HashMap::new(),
            default_route: None,
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
}

impl Default for RoutingTable {
    fn default() -> Self {
        Self::new()
    }
}
