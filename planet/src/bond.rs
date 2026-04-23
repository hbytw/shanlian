//! Bond 多链路聚合模块
//!
//! 负责管理多条隧道链路的带宽聚合和故障切换

use anyhow::Result;
use std::sync::Arc;
use tokio::sync::RwLock;
use tokio::time::{interval, Duration};

/// Bond 模式
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum BondMode {
    /// 轮询模式
    RoundRobin,
    /// 加权轮询
    WeightedRoundRobin,
    /// 最低延迟
    LeastLatency,
    /// 主备切换
    Failover,
}

impl Default for BondMode {
    fn default() -> Self {
        Self::Failover
    }
}

impl std::fmt::Display for BondMode {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            BondMode::RoundRobin => write!(f, "balance-rr"),
            BondMode::WeightedRoundRobin => write!(f, "weighted-rr"),
            BondMode::LeastLatency => write!(f, "least-latency"),
            BondMode::Failover => write!(f, "failover"),
        }
    }
}

/// 链路信息
#[derive(Debug)]
pub struct LinkInfo {
    /// 链路 ID
    pub id: String,
    /// 链路名称
    pub name: String,
    /// 延迟 (ms)
    pub rtt_ms: u32,
    /// 丢包率
    pub loss_rate: f32,
    /// 带宽 (Mbps)
    pub bandwidth_mbps: u32,
    /// 状态
    pub status: LinkStatus,
    /// 权重 (用于加权轮询)
    pub weight: u32,
    /// 最后更新时间
    pub last_update: std::time::Instant,
}

/// 链路状态
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum LinkStatus {
    Active,
    Standby,
    Failed,
    Disabled,
}

/// Bond 配置
#[derive(Debug, Clone)]
pub struct BondConfig {
    /// Bond 模式
    pub mode: BondMode,
    /// 是否启用
    pub enabled: bool,
    /// 主链路 ID
    pub primary_link: Option<String>,
    /// 故障切换阈值 (丢包率 %)
    pub failover_threshold_loss_rate: f32,
    /// 故障切换阈值 (延迟 ms)
    pub failover_threshold_rtt_ms: u32,
    /// 监控间隔 (秒)
    pub monitor_interval_secs: u64,
}

impl Default for BondConfig {
    fn default() -> Self {
        Self {
            mode: BondMode::Failover,
            enabled: true,
            primary_link: None,
            failover_threshold_loss_rate: 5.0,
            failover_threshold_rtt_ms: 500,
            monitor_interval_secs: 5,
        }
    }
}

/// 链路质量评估器
#[derive(Debug, Default)]
pub struct LinkQuality {
    /// 综合评分 (0-100)
    pub score: u32,
    /// 延迟评分
    pub rtt_score: u32,
    /// 丢包评分
    pub loss_score: u32,
    /// 带宽评分
    pub bandwidth_score: u32,
}

impl LinkQuality {
    /// 计算综合评分
    pub fn calculate(rtt_ms: u32, loss_rate: f32, bandwidth_mbps: u32) -> Self {
        // 延迟评分: 50ms=100, 500ms=0
        let rtt_score = ((500u32.saturating_sub(rtt_ms)) * 100 / 500).min(100);

        // 丢包评分: 0%=100, 10%=0
        let loss_score = ((100f32 - loss_rate * 10.0) as u32).min(100);

        // 带宽评分: 100Mbps=100
        let bandwidth_score = ((bandwidth_mbps * 100) / 100).min(100);

        // 综合评分
        let score = (rtt_score * 40 + loss_score * 40 + bandwidth_score * 20) / 100;

        Self {
            score,
            rtt_score,
            loss_score,
            bandwidth_score,
        }
    }
}

/// Bond 状态机
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum BondState {
    /// 正常状态
    Normal,
    /// 降级状态
    Degraded,
    /// 故障状态
    Failed,
    /// 恢复中
    Recovering,
}

impl std::fmt::Display for BondState {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            BondState::Normal => write!(f, "正常"),
            BondState::Degraded => write!(f, "降级"),
            BondState::Failed => write!(f, "故障"),
            BondState::Recovering => write!(f, "恢复中"),
        }
    }
}

/// Bond 管理器
pub struct BondManager {
    /// 链路列表
    links: std::collections::HashMap<String, LinkInfo>,
    /// 配置
    config: BondConfig,
    /// 当前状态
    state: BondState,
    /// 轮询索引
    round_robin_index: usize,
    /// 最后检查时间
    last_check: std::time::Instant,
}

impl BondManager {
    pub fn new() -> Self {
        Self {
            links: std::collections::HashMap::new(),
            config: BondConfig::default(),
            state: BondState::Normal,
            round_robin_index: 0,
            last_check: std::time::Instant::now(),
        }
    }

    /// 从配置创建
    pub fn from_config(config: BondConfig) -> Self {
        Self {
            links: std::collections::HashMap::new(),
            config,
            state: BondState::Normal,
            round_robin_index: 0,
            last_check: std::time::Instant::now(),
        }
    }

    /// 获取配置
    pub fn config(&self) -> &BondConfig {
        &self.config
    }

    /// 更新配置
    pub fn set_config(&mut self, config: BondConfig) {
        self.config = config;
        tracing::info!("Bond 配置已更新: {:?}", self.config);
    }

    /// 添加链路
    pub fn add_link(&mut self, id: &str, name: &str, weight: u32) -> Result<()> {
        let link = LinkInfo {
            id: id.to_string(),
            name: name.to_string(),
            rtt_ms: 0,
            loss_rate: 0.0,
            bandwidth_mbps: 0,
            status: LinkStatus::Active,
            weight,
            last_update: std::time::Instant::now(),
        };

        self.links.insert(id.to_string(), link);

        // 如果是第一条链路，设置为主链路
        if self.links.len() == 1 {
            self.config.primary_link = Some(id.to_string());
        }

        tracing::info!("添加 Bond 链路: {} ({})", id, name);
        Ok(())
    }

    /// 移除链路
    pub fn remove_link(&mut self, id: &str) -> Result<()> {
        self.links.remove(id);
        tracing::info!("移除 Bond 链路: {}", id);

        // 如果移除的是主链路，选择新的主链路
        if self.config.primary_link.as_deref() == Some(id) {
            self.config.primary_link = self.links.keys().next().map(|s| s.clone());
        }

        Ok(())
    }

    /// 更新链路质量
    pub fn update_link_quality(
        &mut self,
        id: &str,
        rtt_ms: u32,
        loss_rate: f32,
        bandwidth_mbps: u32,
    ) -> Result<()> {
        if let Some(link) = self.links.get_mut(id) {
            link.rtt_ms = rtt_ms;
            link.loss_rate = loss_rate;
            link.bandwidth_mbps = bandwidth_mbps;
            link.last_update = std::time::Instant::now();

            tracing::debug!(
                "链路 {} 质量更新: rtt={}ms loss={:.1}% bw={}Mbps",
                id, rtt_ms, loss_rate, bandwidth_mbps
            );
        }
        Ok(())
    }

    /// 获取链路信息
    pub fn get_link(&self, id: &str) -> Option<&LinkInfo> {
        self.links.get(id)
    }

    /// 获取所有链路
    pub fn list_links(&self) -> Vec<&LinkInfo> {
        self.links.values().collect()
    }

    /// 获取活动链路
    pub fn get_active_links(&self) -> Vec<&LinkInfo> {
        self.links
            .values()
            .filter(|l| l.status == LinkStatus::Active)
            .collect()
    }

    /// 获取主链路
    pub fn get_primary_link(&self) -> Option<&LinkInfo> {
        self.config
            .primary_link
            .as_ref()
            .and_then(|id| self.links.get(id))
    }

    /// 获取最佳链路 (根据模式)
    pub fn select_link(&mut self) -> Option<String> {
        if !self.config.enabled {
            return self.config.primary_link.clone();
        }

        match self.config.mode {
            BondMode::RoundRobin => self.select_round_robin(),
            BondMode::WeightedRoundRobin => self.select_weighted_round_robin(),
            BondMode::LeastLatency => self.select_least_latency(),
            BondMode::Failover => self.select_failover(),
        }
    }

    /// 轮询选择
    fn select_round_robin(&mut self) -> Option<String> {
        let ids: Vec<String> = self.get_active_links()
            .iter()
            .map(|l| l.id.clone())
            .collect();
        if ids.is_empty() {
            return None;
        }

        let index = self.round_robin_index % ids.len();
        self.round_robin_index += 1;

        Some(ids[index].clone())
    }

    /// 加权轮询选择
    fn select_weighted_round_robin(&self) -> Option<String> {
        let active_links: Vec<_> = self.get_active_links().into_iter().collect();
        if active_links.is_empty() {
            return None;
        }

        // 计算总权重
        let total_weight: u32 = active_links.iter().map(|l| l.weight).sum();
        if total_weight == 0 {
            return active_links.first().map(|l| l.id.clone());
        }

        // 随机选择
        use rand::Rng;
        let mut rng = rand::thread_rng();
        let target = rng.gen_range(0..total_weight);

        let mut accumulated = 0u32;
        for link in &active_links {
            accumulated += link.weight;
            if accumulated > target {
                return Some(link.id.clone());
            }
        }

        active_links.last().map(|l| l.id.clone())
    }

    /// 最低延迟选择
    fn select_least_latency(&self) -> Option<String> {
        self.links
            .values()
            .filter(|l| l.status == LinkStatus::Active)
            .min_by_key(|l| l.rtt_ms)
            .map(|l| l.id.clone())
    }

    /// 主备切换选择
    fn select_failover(&self) -> Option<String> {
        // 优先使用主链路
        if let Some(primary) = &self.config.primary_link {
            if let Some(link) = self.links.get(primary) {
                if link.status == LinkStatus::Active && self.is_link_healthy(link) {
                    return Some(link.id.clone());
                }
            }
        }

        // 主链路不可用，选择备用链路
        self.get_active_links()
            .into_iter()
            .find(|l| self.is_link_healthy(l))
            .map(|l| l.id.clone())
    }

    /// 检查链路是否健康
    fn is_link_healthy(&self, link: &LinkInfo) -> bool {
        link.loss_rate < self.config.failover_threshold_loss_rate
            && link.rtt_ms < self.config.failover_threshold_rtt_ms
    }

    /// 检查链路状态
    pub fn check_links(&mut self) {
        let now = std::time::Instant::now();

        // 复制阈值以避免在迭代过程中借用 self
        let threshold_rtt = self.config.failover_threshold_rtt_ms;
        let threshold_loss = self.config.failover_threshold_loss_rate;

        for link in self.links.values_mut() {
            // 检查链路是否超时
            if now.duration_since(link.last_update).as_secs() > 60 {
                link.status = LinkStatus::Failed;
                tracing::warn!("链路 {} 超时，标记为失败", link.id);
                continue;
            }

            // 检查链路质量 - 使用预先复制的阈值避免borrow冲突
            let is_healthy = link.rtt_ms < threshold_rtt
                && link.loss_rate < threshold_loss;

            if !is_healthy {
                if link.status == LinkStatus::Active {
                    link.status = LinkStatus::Standby;
                    tracing::warn!(
                        "链路 {} 质量下降: rtt={}ms loss={:.1}%",
                        link.id, link.rtt_ms, link.loss_rate
                    );
                }
            } else if link.status == LinkStatus::Standby {
                link.status = LinkStatus::Active;
                tracing::info!("链路 {} 恢复", link.id);
            }
        }

        // 更新 Bond 状态
        self.update_state();
        self.last_check = now;
    }

    /// 更新 Bond 状态机
    fn update_state(&mut self) {
        let active_count = self.links.values().filter(|l| l.status == LinkStatus::Active).count();

        let new_state = if active_count == 0 {
            BondState::Failed
        } else if active_count < self.links.len() && active_count > 0 {
            BondState::Degraded
        } else if self.state == BondState::Degraded || self.state == BondState::Failed {
            BondState::Recovering
        } else {
            BondState::Normal
        };

        if new_state != self.state {
            tracing::info!("Bond 状态变更: {} -> {}", self.state, new_state);
            self.state = new_state;
        }
    }

    /// 获取当前状态
    pub fn state(&self) -> BondState {
        self.state
    }

    /// 获取 Bond 统计信息
    pub fn stats(&self) -> BondStats {
        let total_bandwidth: u32 = self.links
            .values()
            .filter(|l| l.status == LinkStatus::Active)
            .map(|l| l.bandwidth_mbps)
            .sum();

        let count = self.get_active_links().len();
        let avg_rtt: u32 = if count > 0 {
            self.links
                .values()
                .filter(|l| l.status == LinkStatus::Active)
                .map(|l| l.rtt_ms)
                .sum::<u32>() / count as u32
        } else {
            0
        };

        BondStats {
            total_links: self.links.len(),
            active_links: count,
            state: self.state,
            total_bandwidth_mbps: total_bandwidth,
            average_rtt_ms: avg_rtt,
        }
    }
}

impl Default for BondManager {
    fn default() -> Self {
        Self::new()
    }
}

/// Bond 统计信息
#[derive(Debug)]
pub struct BondStats {
    pub total_links: usize,
    pub active_links: usize,
    pub state: BondState,
    pub total_bandwidth_mbps: u32,
    pub average_rtt_ms: u32,
}

/// Bond Session (用于跟踪多链路会话)
pub struct BondSession {
    /// 会话 ID
    pub id: String,
    /// 使用的链路 ID
    pub link_id: String,
    /// 创建时间
    pub created_at: std::time::Instant,
    /// 发送字节数
    pub send_bytes: u64,
    /// 接收字节数
    pub recv_bytes: u64,
}

impl BondSession {
    pub fn new(link_id: &str) -> Self {
        Self {
            id: uuid::Uuid::new_v4().to_string(),
            link_id: link_id.to_string(),
            created_at: std::time::Instant::now(),
            send_bytes: 0,
            recv_bytes: 0,
        }
    }
}
