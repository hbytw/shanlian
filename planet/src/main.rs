//! 闪连 VPN Planet 客户端
//!
//! Planet 是闪连 VPN 的客户端程序，负责：
//! - 连接 Controller 进行设备注册和认证
//! - 建立到 Moon 或其他 Planet 的隧道
//! - 管理 TUN 虚拟网卡
//! - 执行路由和策略

use anyhow::Context;
use clap::{Args, Parser, Subcommand};
use std::sync::Arc;
use tracing_subscriber::{layer::SubscriberExt, util::SubscriberInitExt};
use tun::TunDevice;
use vpn::{VpnConfig, VpnEngine};

mod audit;
mod bond;
mod cli;
pub mod config;
pub mod console;
pub mod controller;
pub mod crypto;
pub mod nat;
pub mod policy;
pub mod routing;
pub mod session;
pub mod tunnel;
mod tun;
pub mod vpn;

pub use config::Config;
pub use crypto::{KeyPair, Crypto};
pub use tunnel::{TunnelManager, TunnelType};
pub use controller::ControllerClient;
pub use bond::{BondManager, BondState, BondMode};
pub use session::SessionManager;
pub use policy::{PolicyEngine, PolicyRule, PolicyType, PolicyAction};
pub use nat::{NatHandler, NatType, NatInfo};
pub use routing::RoutingTable;

#[derive(Parser, Debug)]
#[command(name = "planet")]
#[command(about = "闪连 VPN Planet 客户端", long_about = None)]
struct Cli {
    #[command(subcommand)]
    command: Commands,
}

#[derive(Subcommand, Debug)]
enum Commands {
    /// 连接到 VPN
    Connect {
        /// 服务器地址
        #[arg(short, long)]
        server: Option<String>,
    },
    /// 断开 VPN 连接
    Disconnect,
    /// 显示连接状态
    Status,
    /// 系统信息
    System {
        #[command(subcommand)]
        sub: Option<SystemCommands>,
    },
    /// 隧道管理
    Tunnel {
        #[command(subcommand)]
        sub: TunnelCommands,
    },
    /// 节点管理
    Node {
        #[command(subcommand)]
        sub: NodeCommands,
    },
    /// BOND 链路聚合管理
    Bond {
        #[command(subcommand)]
        sub: BondCommands,
    },
    /// NAT 类型检测
    Nat {
        #[command(subcommand)]
        sub: Option<NatCommands>,
    },
    /// 路由管理
    Route {
        #[command(subcommand)]
        sub: RouteCommands,
    },
    /// 策略管理
    Policy {
        #[command(subcommand)]
        sub: PolicyCommands,
    },
    /// 配置管理
    Config {
        #[command(subcommand)]
        sub: Option<ConfigCommands>,
    },
    /// 交互式控制台
    Console,
    /// 显示版本
    Version,
}

#[derive(Subcommand, Debug)]
enum SystemCommands {
    /// 显示系统信息
    Info,
    /// 显示网络接口
    Interfaces,
    /// 显示 DNS 配置
    Dns,
    /// 显示日志
    Logs {
        /// 日志行数
        #[arg(default_value = "50")]
        lines: usize,
    },
}

#[derive(Subcommand, Debug)]
enum TunnelCommands {
    /// 列出所有隧道
    List,
    /// 显示隧道详情
    Info {
        /// 隧道 ID
        id: String,
    },
    /// 创建隧道
    Create {
        /// 隧道类型 (udp/tcp/wss/moon)
        #[arg(short, long, default_value = "moon")]
        tunnel_type: String,
        /// 远程地址
        #[arg(short, long)]
        remote: String,
    },
    /// 删除隧道
    Delete {
        /// 隧道 ID
        id: String,
    },
    /// 连接隧道
    Connect {
        /// 隧道 ID
        id: String,
    },
    /// 断开隧道
    Disconnect {
        /// 隧道 ID
        id: String,
    },
    /// 测试隧道延迟
    Ping {
        /// 隧道 ID
        id: String,
    },
}

#[derive(Subcommand, Debug)]
enum NodeCommands {
    /// 列出所有节点
    List,
    /// 显示节点详情
    Info {
        /// 节点 ID
        id: String,
    },
    /// 测试节点延迟
    Ping {
        /// 节点 ID
        id: String,
    },
}

#[derive(Subcommand, Debug)]
enum BondCommands {
    /// 显示 BOND 状态
    Status,
    /// 列出所有链路
    Links,
    /// 添加链路
    Add {
        /// 链路 ID
        #[arg(short, long)]
        id: String,
        /// 链路名称
        #[arg(short, long)]
        name: String,
        /// 链路权重
        #[arg(short, long, default_value = "1")]
        weight: u32,
    },
    /// 删除链路
    Remove {
        /// 链路 ID
        id: String,
    },
    /// 更新链路质量
    Update {
        /// 链路 ID
        id: String,
        /// 延迟 (ms)
        #[arg(short, long)]
        rtt_ms: u32,
        /// 丢包率
        #[arg(short, long)]
        loss_rate: f32,
        /// 带宽 (Mbps)
        #[arg(short, long)]
        bandwidth: u32,
    },
    /// 设置 BOND 模式
    Mode {
        /// 模式 (round-robin/weighted-rr/least-latency/failover)
        #[arg(short, long)]
        mode: String,
    },
}

#[derive(Subcommand, Debug)]
enum NatCommands {
    /// 检测 NAT 类型
    Detect,
    /// 显示 NAT 状态
    Status,
}

#[derive(Subcommand, Debug)]
enum RouteCommands {
    /// 列出路由表
    List,
    /// 添加路由
    Add {
        /// 目标网络 (CIDR)
        dest: String,
        /// 网关地址
        gateway: String,
    },
    /// 删除路由
    Remove {
        /// 目标网络 (CIDR)
        dest: String,
    },
    /// 清除所有路由
    Flush,
}

#[derive(Subcommand, Debug)]
enum PolicyCommands {
    /// 列出所有策略
    List,
    /// 添加路由策略
    AddRoute {
        /// 策略名称
        #[arg(short, long)]
        name: String,
        /// 目标网络 (CIDR)
        dest: String,
        /// 网关地址
        gateway: String,
        /// 优先级
        #[arg(short, long, default_value = "100")]
        priority: u32,
    },
    /// 添加 DNS 策略
    AddDns {
        /// 策略名称
        #[arg(short, long)]
        name: String,
        /// 域名
        domain: String,
        /// 动作 (allow/deny)
        #[arg(short, long)]
        action: String,
        /// 优先级
        #[arg(short, long, default_value = "100")]
        priority: u32,
    },
    /// 添加 URL 过滤策略
    AddUrlFilter {
        /// 策略名称
        #[arg(short, long)]
        name: String,
        /// URL 模式
        url: String,
        /// 动作 (allow/deny)
        #[arg(short, long)]
        action: String,
        /// 优先级
        #[arg(short, long, default_value = "100")]
        priority: u32,
    },
    /// 封禁域名
    BlockDomain {
        /// 域名
        domain: String,
    },
    /// 解封域名
    UnblockDomain {
        /// 域名
        domain: String,
    },
    /// 删除策略
    Remove {
        /// 策略 ID
        id: String,
    },
    /// 清除所有策略
    Flush,
    /// 显示策略统计
    Stats,
}

#[derive(Subcommand, Debug)]
enum ConfigCommands {
    /// 显示当前配置
    Show,
    /// 设置配置项
    Set {
        /// 配置键
        key: String,
        /// 配置值
        value: String,
    },
    /// 重置配置
    Reset,
    /// 导出配置
    Export {
        /// 输出文件路径
        path: Option<String>,
    },
    /// 导入配置
    Import {
        /// 输入文件路径
        path: String,
    },
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let cli = Cli::parse();

    // 初始化日志
    let env_filter = tracing_subscriber::EnvFilter::try_from_default_env()
        .unwrap_or_else(|_| "info".into());

    let fmt_layer = tracing_subscriber::fmt::layer()
        .with_target(true)
        .with_thread_ids(true);

    tracing_subscriber::registry()
        .with(env_filter)
        .with(fmt_layer)
        .init();

    match cli.command {
        Commands::Connect { server } => {
            let config = Config::load(None)?;
            let server = server.unwrap_or(config.controller.clone());
            println!("正在连接到 {}", server);

            // 创建设备 ID
            let device_id = config.device_id.clone()
                .unwrap_or_else(|| {
                    let id = uuid::Uuid::new_v4().to_string();
                    println!("生成新设备 ID: {}", id);
                    id
                });

            // 创建 Controller 客户端并注册
            let mut controller = ControllerClient::new(&server, &device_id).await
                .context("创建 Controller 客户端失败")?;

            let register_resp = controller.register().await
                .context("设备注册失败")?;

            println!("\n注册成功!");
            println!("  Peer ID: {}", register_resp.peer_id);
            println!("  分配 IP: {}", register_resp.assigned_ip);
            println!("  网络 ID: {}", register_resp.network_id);

            // 显示 Moon 节点
            if let Some(ref moon_list) = register_resp.moon_list {
                if !moon_list.is_empty() {
                    println!("\n可用 Moon 节点:");
                    for moon in moon_list.iter().take(3) {
                        println!("  - {}:{} (online: {})", moon.domain, moon.relay_port, moon.online);
                    }
                }
            }

            // 创建 TUN 设备
            println!("\n创建 TUN 设备...");
            let tun = TunDevice::new(&config.tun_name, config.tun_mtu).await
                .context("创建 TUN 设备失败")?;

            tun.set_ip(&register_resp.assigned_ip, "255.255.255.0").await
                .context("设置 IP 失败")?;
            tun.up().await
                .context("启动 TUN 设备失败")?;
            println!("TUN 设备 {} 已启动", config.tun_name);

            // 连接 Moon 隧道
            println!("\n连接到 Moon 节点...");
            let mut tunnel_manager = TunnelManager::new();

            if let Some(ref moon_list) = register_resp.moon_list {
                let mut connected = false;
                for moon in moon_list.iter().filter(|m| m.online) {
                    let moon_addr = format!("{}:{}", moon.domain, moon.relay_port);
                    println!("  尝试连接: {}", moon_addr);

                    match tunnel_manager.create(TunnelType::Moon, &moon_addr).await {
                        Ok(id) => {
                            match tunnel_manager.connect(&id).await {
                                Ok(_) => {
                                    if let Err(e) = tunnel_manager.set_active(&id) {
                                        println!("  设置活动隧道失败: {}", e);
                                        continue;
                                    }
                                    println!("  已连接到 {}", moon_addr);
                                    connected = true;
                                    break;
                                }
                                Err(e) => {
                                    println!("  连接失败: {}", e);
                                    continue;
                                }
                            }
                        }
                        Err(e) => {
                            println!("  创建隧道失败: {}", e);
                            continue;
                        }
                    }
                }

                if !connected {
                    println!("无法连接到任何 Moon 节点");
                    return Ok(());
                }
            }

            // 创建并运行 VPN 引擎
            let vpn_config = VpnConfig {
                heartbeat_interval_secs: config.heartbeat_interval,
                tun_mtu: config.tun_mtu,
                tun_name: config.tun_name.clone(),
                packet_buf_size: 65535,
            };

            let vpn_engine = VpnEngine::new(tun, tunnel_manager, vpn_config).await
                .context("创建 VPN 引擎失败")?;

            let engine_arc = Arc::new(vpn_engine);
            let engine_for_task = engine_arc.clone();

            println!("\n启动 VPN 数据平面...");

            // 处理 Ctrl-C
            let engine_for_signal = engine_arc.clone();
            tokio::spawn(async move {
                let _ = tokio::signal::ctrl_c().await;
                println!("\n收到中断信号，正在关闭...");
                engine_for_signal.shutdown();
            });

            // 运行引擎
            if let Err(e) = engine_for_task.run().await {
                tracing::error!("VPN 引擎错误: {}", e);
            }

            println!("VPN 连接已关闭");
        }
        Commands::Disconnect => {
            println!("断开 VPN 连接...");
            println!("断开功能开发中...");
        }
        Commands::Status => {
            show_status().await?;
        }
        Commands::System { sub } => {
            match sub {
                Some(SystemCommands::Info) => show_system_info().await?,
                Some(SystemCommands::Interfaces) => show_interfaces().await?,
                Some(SystemCommands::Dns) => show_dns().await?,
                Some(SystemCommands::Logs { lines }) => show_logs(lines).await?,
                None => show_system_info().await?,
            }
        }
        Commands::Tunnel { sub } => {
            match sub {
                TunnelCommands::List => list_tunnels().await?,
                TunnelCommands::Info { id } => tunnel_info(&id).await?,
                TunnelCommands::Create { tunnel_type, remote } => create_tunnel(&tunnel_type, &remote).await?,
                TunnelCommands::Delete { id } => delete_tunnel(&id).await?,
                TunnelCommands::Connect { id } => connect_tunnel(&id).await?,
                TunnelCommands::Disconnect { id } => disconnect_tunnel(&id).await?,
                TunnelCommands::Ping { id } => ping_tunnel(&id).await?,
            }
        }
        Commands::Node { sub } => {
            match sub {
                NodeCommands::List => list_nodes().await?,
                NodeCommands::Info { id } => node_info(&id).await?,
                NodeCommands::Ping { id } => ping_node(&id).await?,
            }
        }
        Commands::Bond { sub } => {
            match sub {
                BondCommands::Status => bond_status().await?,
                BondCommands::Links => list_links().await?,
                BondCommands::Add { id, name, weight } => add_link(&id, &name, weight).await?,
                BondCommands::Remove { id } => remove_link(&id).await?,
                BondCommands::Update { id, rtt_ms, loss_rate, bandwidth } => update_link(&id, rtt_ms, loss_rate, bandwidth).await?,
                BondCommands::Mode { mode } => set_bond_mode(&mode).await?,
            }
        }
        Commands::Nat { sub } => {
            match sub {
                Some(NatCommands::Detect) => detect_nat().await?,
                Some(NatCommands::Status) => nat_status().await?,
                None => detect_nat().await?,
            }
        }
        Commands::Route { sub } => {
            match sub {
                RouteCommands::List => list_routes().await?,
                RouteCommands::Add { dest, gateway } => add_route(&dest, &gateway).await?,
                RouteCommands::Remove { dest } => remove_route(&dest).await?,
                RouteCommands::Flush => flush_routes().await?,
            }
        }
        Commands::Policy { sub } => {
            match sub {
                PolicyCommands::List => list_policies().await?,
                PolicyCommands::AddRoute { name, dest, gateway, priority } => add_route_policy(&name, &dest, &gateway, priority).await?,
                PolicyCommands::AddDns { name, domain, action, priority } => add_dns_policy(&name, &domain, &action, priority).await?,
                PolicyCommands::AddUrlFilter { name, url, action, priority } => add_url_policy(&name, &url, &action, priority).await?,
                PolicyCommands::BlockDomain { domain } => block_domain(&domain).await?,
                PolicyCommands::UnblockDomain { domain } => unblock_domain(&domain).await?,
                PolicyCommands::Remove { id } => remove_policy(&id).await?,
                PolicyCommands::Flush => flush_policies().await?,
                PolicyCommands::Stats => policy_stats().await?,
            }
        }
        Commands::Config { sub } => {
            match sub {
                Some(ConfigCommands::Show) => show_config().await?,
                Some(ConfigCommands::Set { key, value }) => set_config(&key, &value).await?,
                Some(ConfigCommands::Reset) => reset_config().await?,
                Some(ConfigCommands::Export { path }) => export_config(path.as_deref()).await?,
                Some(ConfigCommands::Import { path }) => import_config(&path).await?,
                None => show_config().await?,
            }
        }
        Commands::Console => {
            let config = Config::load(None)?;
            console::start_interactive(config).await?;
        }
        Commands::Version => {
            println!("闪连 VPN Planet 客户端");
            println!("版本: {}", env!("CARGO_PKG_VERSION"));
            println!("构建时间: {}", chrono::Local::now().format("%Y-%m-%d %H:%M:%S"));
        }
    }

    Ok(())
}

// ============ System Commands ============

async fn show_status() -> anyhow::Result<()> {
    println!("\n=== 连接状态 ===");
    println!("  状态: 未连接 (功能开发中)");
    println!("  服务器: --");
    println!("  分配 IP: --");
    println!("  运行时长: --");
    println!("  最后错误: --");
    Ok(())
}

async fn show_system_info() -> anyhow::Result<()> {
    println!("\n=== 系统信息 ===");
    println!("  主机名: {}", hostname::get()
        .map(|h| h.to_string_lossy().to_string())
        .unwrap_or_else(|_| "unknown".to_string()));
    println!("  操作系统: {}", std::env::consts::OS);
    println!("  架构: {}", std::env::consts::ARCH);
    println!("  进程 ID: {}", std::process::id());
    println!("  版本: {}", env!("CARGO_PKG_VERSION"));
    Ok(())
}

async fn show_interfaces() -> anyhow::Result<()> {
    println!("\n=== 网络接口 ===");
    println!("  TUN 设备: planet0 (未连接)");
    println!("  本地网络: --");
    Ok(())
}

async fn show_dns() -> anyhow::Result<()> {
    println!("\n=== DNS 配置 ===");
    println!("  DNS 服务器: --");
    println!("  搜索域: --");
    println!("  DNS 劫持: --");
    Ok(())
}

async fn show_logs(lines: usize) -> anyhow::Result<()> {
    println!("\n=== 最近 {} 条日志 ===", lines);
    let exe_dir = std::env::current_exe()
        .ok()
        .and_then(|p| p.parent().map(|p| p.to_path_buf()))
        .unwrap_or_else(|| std::env::temp_dir());
    let log_file = exe_dir.join("logs").join("planet-gui.log");

    if log_file.exists() {
        let content = std::fs::read_to_string(&log_file)?;
        let all_lines: Vec<&str> = content.lines().collect();
        let start = if all_lines.len() > lines { all_lines.len() - lines } else { 0 };
        for line in &all_lines[start..] {
            println!("{}", line);
        }
    } else {
        println!("  (无日志文件)");
    }
    Ok(())
}

// ============ Tunnel Commands ============

async fn list_tunnels() -> anyhow::Result<()> {
    println!("\n=== 隧道列表 ===");
    println!("  ID                                   类型    远程地址              状态");
    println!("  --                                   ----    -----------          ------");
    println!("  (暂无隧道)");
    Ok(())
}

async fn tunnel_info(id: &str) -> anyhow::Result<()> {
    println!("\n=== 隧道 {} 详情 ===", id);
    println!("  隧道 ID: {}", id);
    println!("  类型: --");
    println!("  远程地址: --");
    println!("  状态: --");
    println!("  延迟: --");
    println!("  发送: --");
    println!("  接收: --");
    Ok(())
}

async fn create_tunnel(tunnel_type: &str, remote: &str) -> anyhow::Result<()> {
    println!("\n创建隧道...");
    println!("  类型: {}", tunnel_type);
    println!("  远程: {}", remote);
    println!("  隧道创建功能开发中...");
    Ok(())
}

async fn delete_tunnel(id: &str) -> anyhow::Result<()> {
    println!("\n删除隧道 {}...", id);
    println!("  隧道删除功能开发中...");
    Ok(())
}

async fn connect_tunnel(id: &str) -> anyhow::Result<()> {
    println!("\n连接隧道 {}...", id);
    println!("  隧道连接功能开发中...");
    Ok(())
}

async fn disconnect_tunnel(id: &str) -> anyhow::Result<()> {
    println!("\n断开隧道 {}...", id);
    println!("  隧道断开功能开发中...");
    Ok(())
}

async fn ping_tunnel(id: &str) -> anyhow::Result<()> {
    println!("\n测试隧道 {} 延迟...", id);
    println!("  延迟: -- ms");
    println!("  丢包: --%");
    Ok(())
}

// ============ Node Commands ============

async fn list_nodes() -> anyhow::Result<()> {
    println!("\n=== 节点列表 ===");
    println!("  ID                                   域名/地址              在线  延迟");
    println!("  --                                   ---------------       ----  ----");
    println!("  (暂无节点)");
    Ok(())
}

async fn node_info(id: &str) -> anyhow::Result<()> {
    println!("\n=== 节点 {} 详情 ===", id);
    println!("  节点 ID: {}", id);
    println!("  地址: --");
    println!("  在线状态: --");
    println!("  延迟: -- ms");
    Ok(())
}

async fn ping_node(id: &str) -> anyhow::Result<()> {
    println!("\n测试节点 {} 延迟...", id);
    println!("  延迟: -- ms");
    println!("  丢包: --%");
    Ok(())
}

// ============ Bond Commands ============

async fn bond_status() -> anyhow::Result<()> {
    println!("\n=== BOND 状态 ===");
    println!("  模式: failover");
    println!("  状态: 正常");
    println!("  总链路: 0");
    println!("  活动链路: 0");
    println!("  总带宽: -- Mbps");
    println!("  平均延迟: -- ms");
    Ok(())
}

async fn list_links() -> anyhow::Result<()> {
    println!("\n=== BOND 链路 ===");
    println!("  ID         名称          状态    延迟(ms)  丢包(%)  带宽(Mbps)  权重");
    println!("  --         ----          ------   ---------  --------  ----------  ----");
    println!("  (暂无链路)");
    Ok(())
}

async fn add_link(id: &str, name: &str, weight: u32) -> anyhow::Result<()> {
    println!("\n添加链路...");
    println!("  ID: {}", id);
    println!("  名称: {}", name);
    println!("  权重: {}", weight);
    Ok(())
}

async fn remove_link(id: &str) -> anyhow::Result<()> {
    println!("\n移除链路 {}...", id);
    Ok(())
}

async fn update_link(id: &str, rtt_ms: u32, loss_rate: f32, bandwidth: u32) -> anyhow::Result<()> {
    println!("\n更新链路 {} 质量...", id);
    println!("  延迟: {} ms", rtt_ms);
    println!("  丢包: {}%", loss_rate);
    println!("  带宽: {} Mbps", bandwidth);
    Ok(())
}

async fn set_bond_mode(mode: &str) -> anyhow::Result<()> {
    println!("\n设置 BOND 模式为: {}", mode);
    Ok(())
}

// ============ NAT Commands ============

async fn detect_nat() -> anyhow::Result<()> {
    println!("\n=== NAT 类型检测 ===");
    println!("  正在检测，请稍候...");

    let mut handler = NatHandler::new();

    match handler.detect().await {
        Ok(info) => {
            println!("\n  NAT 类型: {}", info.nat_type);
            println!("  公网 IP: {}", info.public_ip);
            println!("  公网端口: {}", info.public_port);
            println!("  本地 IP: {}", info.local_ip);
            println!("  本地端口: {}", info.local_port);
            println!("  支持 P2P: {}", if info.hole_punchable { "是" } else { "否" });
        }
        Err(e) => {
            println!("\n  检测失败: {}", e);
        }
    }
    Ok(())
}

async fn nat_status() -> anyhow::Result<()> {
    println!("\n=== NAT 状态 ===");
    println!("  NAT 类型: --");
    println!("  公网地址: --");
    println!("  可打洞: --");
    Ok(())
}

// ============ Route Commands ============

async fn list_routes() -> anyhow::Result<()> {
    println!("\n=== 路由表 ===");
    println!("  目标网络          网关            接口      度量值");
    println!("  ---------------   -------------   --------   ------");
    println!("  (暂无路由)");
    Ok(())
}

async fn add_route(dest: &str, gateway: &str) -> anyhow::Result<()> {
    println!("\n添加路由...");
    println!("  目标: {}", dest);
    println!("  网关: {}", gateway);
    Ok(())
}

async fn remove_route(dest: &str) -> anyhow::Result<()> {
    println!("\n删除路由 {}...", dest);
    Ok(())
}

async fn flush_routes() -> anyhow::Result<()> {
    println!("\n清除所有路由...");
    Ok(())
}

// ============ Policy Commands ============

async fn list_policies() -> anyhow::Result<()> {
    println!("\n=== 策略列表 ===");
    println!("  ID    名称                类型      目标               动作    优先级");
    println!("  --    ----                ----      -----               ------   ------");
    println!("  (暂无策略)");
    Ok(())
}

async fn add_route_policy(name: &str, dest: &str, gateway: &str, priority: u32) -> anyhow::Result<()> {
    println!("\n添加路由策略...");
    println!("  名称: {}", name);
    println!("  目标: {}", dest);
    println!("  网关: {}", gateway);
    println!("  优先级: {}", priority);
    Ok(())
}

async fn add_dns_policy(name: &str, domain: &str, action: &str, priority: u32) -> anyhow::Result<()> {
    println!("\n添加 DNS 策略...");
    println!("  名称: {}", name);
    println!("  域名: {}", domain);
    println!("  动作: {}", action);
    println!("  优先级: {}", priority);
    Ok(())
}

async fn add_url_policy(name: &str, url: &str, action: &str, priority: u32) -> anyhow::Result<()> {
    println!("\n添加 URL 过滤策略...");
    println!("  名称: {}", name);
    println!("  URL: {}", url);
    println!("  动作: {}", action);
    println!("  优先级: {}", priority);
    Ok(())
}

async fn block_domain(domain: &str) -> anyhow::Result<()> {
    println!("\n封禁域名: {}", domain);
    Ok(())
}

async fn unblock_domain(domain: &str) -> anyhow::Result<()> {
    println!("\n解封域名: {}", domain);
    Ok(())
}

async fn remove_policy(id: &str) -> anyhow::Result<()> {
    println!("\n删除策略 {}...", id);
    Ok(())
}

async fn flush_policies() -> anyhow::Result<()> {
    println!("\n清除所有策略...");
    Ok(())
}

async fn policy_stats() -> anyhow::Result<()> {
    println!("\n=== 策略统计 ===");
    println!("  总规则数: 0");
    println!("  活动规则: 0");
    println!("  封禁域名: 0");
    println!("  超限配额: 0");
    Ok(())
}

// ============ Config Commands ============

async fn show_config() -> anyhow::Result<()> {
    println!("\n=== 当前配置 ===");
    let config = Config::load(None)?;
    println!("  Controller: {}", config.controller);
    println!("  设备 ID: {}", config.device_id.unwrap_or_else(|| "(自动)".to_string()));
    println!("  TUN 设备: {}", config.tun_name);
    println!("  心跳间隔: {} 秒", config.heartbeat_interval);
    Ok(())
}

async fn set_config(key: &str, value: &str) -> anyhow::Result<()> {
    println!("\n设置配置: {} = {}", key, value);
    println!("  配置设置功能开发中...");
    Ok(())
}

async fn reset_config() -> anyhow::Result<()> {
    println!("\n重置配置...");
    Ok(())
}

async fn export_config(path: Option<&str>) -> anyhow::Result<()> {
    let path = path.unwrap_or("planet-config.json");
    println!("\n导出配置到: {}", path);
    Ok(())
}

async fn import_config(path: &str) -> anyhow::Result<()> {
    println!("\n从 {} 导入配置...", path);
    Ok(())
}
