//! NAT 类型检测和打洞模块

use anyhow::Result;

/// NAT 类型枚举
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NatType {
    /// 开放网络 - 无 NAT
    Open,
    /// 全锥型 NAT
    FullCone,
    /// 限制锥型 NAT
    RestrictedCone,
    /// 端口限制锥型 NAT
    PortRestrictedCone,
    /// 对称 NAT - 无法打洞
    Symmetric,
}

impl std::fmt::Display for NatType {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            NatType::Open => write!(f, "开放 (无 NAT)"),
            NatType::FullCone => write!(f, "全锥型 NAT"),
            NatType::RestrictedCone => write!(f, "限制锥型 NAT"),
            NatType::PortRestrictedCone => write!(f, "端口限制锥型 NAT"),
            NatType::Symmetric => write!(f, "对称 NAT (无法 P2P)"),
        }
    }
}

/// NAT 检测结果
#[derive(Debug)]
pub struct NatInfo {
    /// NAT 类型
    pub nat_type: NatType,
    /// 公网 IP
    pub public_ip: String,
    /// 公网端口
    pub public_port: u16,
    /// 是否可打洞
    pub hole_punchable: bool,
}

/// NAT 处理器
pub struct NatHandler {
    stun_servers: Vec<(String, u16)>,
}

impl NatHandler {
    pub fn new() -> Self {
        Self {
            stun_servers: vec![
                ("stun.qq.com".to_string(), 3478),
                ("stun.l.google.com".to_string(), 19302),
            ],
        }
    }

    /// 检测 NAT 类型
    pub async fn detect(&self) -> Result<NatInfo> {
        // TODO: 实现完整的 NAT 类型检测
        // 1. 首先获取公网 IP 和端口
        // 2. 然后尝试不同类型的 STUN 查询来确定 NAT 类型
        // 3. 返回检测结果

        let (public_ip, public_port) = self.query_public_ip().await?;

        // 简化的 NAT 类型检测 - 实际需要多次 STUN 查询
        let nat_type = self.classify_nat_type().await?;

        Ok(NatInfo {
            nat_type,
            public_ip,
            public_port,
            hole_punchable: nat_type != NatType::Symmetric,
        })
    }

    /// 查询公网 IP 和端口
    async fn query_public_ip(&self) -> Result<(String, u16)> {
        // TODO: 实现 STUN 查询
        // 使用 STUN Binding Request 获取公网地址
        Ok(("0.0.0.0".to_string(), 0))
    }

    /// 根据 STUN 结果分类 NAT 类型
    async fn classify_nat_type(&self) -> Result<NatType> {
        // TODO: 实现完整的 NAT 类型分类
        // 需要多次测试:
        // 1. 向不同外部地址发送请求，检查端口变化
        // 2. 测试是否支持端口重用
        // 3. 确定具体 NAT 类型
        Ok(NatType::Open)
    }

    /// UDP 打洞
    pub async fn punch_hole(&self, target_ip: &str, target_port: u16) -> Result<()> {
        tracing::info!("开始 UDP 打洞: {}:{}", target_ip, target_port);
        // TODO: 实现 UDP 打洞逻辑
        // 1. 双方互相发送 UDP 包
        // 2. 等待对方包到达
        // 3. 建立直接连接
        Ok(())
    }
}

impl Default for NatHandler {
    fn default() -> Self {
        Self::new()
    }
}
