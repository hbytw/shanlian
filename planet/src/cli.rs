//! CLI 模块 - 命令行接口

use clap::Subcommand;

#[derive(Subcommand, Debug)]
pub enum Commands {
    /// 连接到 VPN
    Connect {
        /// 服务器地址
        #[arg(short, long)]
        server: Option<String>,
    },
    /// 断开 VPN 连接
    Disconnect,
    /// 显示状态
    Status,
    /// 列出节点
    ListNodes,
    /// 列出隧道
    ListTunnels,
    /// 显示 NAT 状态
    NatStatus,
    /// 测试连接延迟
    Ping {
        /// 目标节点 ID
        #[arg(short, long)]
        target: String,
    },
}
