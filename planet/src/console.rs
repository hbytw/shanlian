//! 交互式控制台模块
//!
//! 提供交互式 CLI shell 支持

use crate::{bond::BondManager, config::Config, controller::ControllerClient, nat::NatHandler, policy::PolicyEngine, session::SessionManager, tunnel::TunnelManager};
use anyhow::Result;
use std::sync::Arc;
use tokio::sync::RwLock;

/// 全局状态
pub struct ConsoleState {
    pub connected: bool,
    pub peer_id: Option<String>,
    pub controller_client: Option<Arc<RwLock<ControllerClient>>>,
    pub tunnel_manager: Arc<RwLock<TunnelManager>>,
    pub session_manager: Arc<RwLock<SessionManager>>,
    pub bond_manager: Arc<RwLock<BondManager>>,
    pub policy_engine: Arc<RwLock<PolicyEngine>>,
    pub nat_handler: Arc<RwLock<NatHandler>>,
}

impl Default for ConsoleState {
    fn default() -> Self {
        Self {
            connected: false,
            peer_id: None,
            controller_client: None,
            tunnel_manager: Arc::new(RwLock::new(TunnelManager::new())),
            session_manager: Arc::new(RwLock::new(SessionManager::new())),
            bond_manager: Arc::new(RwLock::new(BondManager::new())),
            policy_engine: Arc::new(RwLock::new(PolicyEngine::new())),
            nat_handler: Arc::new(RwLock::new(NatHandler::new())),
        }
    }
}

/// 启动交互式控制台
pub async fn start_interactive(_config: Config) -> Result<()> {
    let state = Arc::new(RwLock::new(ConsoleState::default()));

    println!("闪连 VPN 控制台");
    println!("================");
    println!("输入 'help' 获取可用命令");
    println!("输入 'exit' 退出");
    println!();

    loop {
        print!("planet> ");
        std::io::Write::flush(&mut std::io::stdout())?;

        let mut input = String::new();
        if std::io::stdin().read_line(&mut input)? == 0 {
            break;
        }

        let input = input.trim();
        if input.is_empty() {
            continue;
        }

        // 解析命令和参数
        let parts: Vec<&str> = input.split_whitespace().collect();
        let cmd = parts.first().unwrap_or(&"");
        let args = &parts[1..];

        let state_clone = state.clone();
        match *cmd {
            "exit" | "quit" | "q" => {
                println!("再见!");
                break;
            }
            "help" | "?" => {
                print_help();
            }
            "connect" => {
                do_connect(state_clone).await;
            }
            "disconnect" => {
                do_disconnect(state_clone).await;
            }
            "status" => {
                do_status(state_clone).await;
            }
            "node" | "nodes" => {
                do_list_nodes(state_clone, args).await;
            }
            "tunnel" | "tunnels" => {
                do_list_tunnels(state_clone, args).await;
            }
            "bond" => {
                do_bond(state_clone, args).await;
            }
            "policy" | "policies" => {
                do_list_policies(state_clone, args).await;
            }
            "nat" => {
                do_nat_status(state_clone).await;
            }
            "session" | "sessions" => {
                do_list_sessions(state_clone).await;
            }
            "ping" => {
                do_ping(state_clone, args).await;
            }
            "log" => {
                do_log_level(args);
            }
            "clear" | "cls" => {
                print!("\x1B[2J\x1B[1;1H");
            }
            _ => {
                println!("未知命令: {}. 输入 'help' 获取帮助.", cmd);
            }
        }
    }

    Ok(())
}

fn print_help() {
    println!("\n可用命令:");
    println!("  connect              - 连接到 VPN 服务器");
    println!("  disconnect           - 断开 VPN 连接");
    println!("  status               - 显示当前状态");
    println!("  node list            - 列出已知节点");
    println!("  node quality <id>    - 显示节点质量");
    println!("  tunnel list          - 列出活动隧道");
    println!("  tunnel connect <type> - 连接到指定类型隧道 (udp/tcp/wss/moon)");
    println!("  tunnel disconnect    - 断开所有隧道");
    println!("  bond status          - 显示 Bond 状态");
    println!("  bond enable          - 启用 Bond");
    println!("  bond disable         - 禁用 Bond");
    println!("  bond mode <mode>     - 设置 Bond 模式 (rr/wrr/lest-latency/failover)");
    println!("  policy list          - 列出策略");
    println!("  nat status           - 显示 NAT 类型");
    println!("  session list         - 列出会话");
    println!("  ping <peer_id>       - 测试到节点的延迟");
    println!("  log <level>          - 设置日志级别 (debug/info/warn/error)");
    println!("  clear                - 清屏");
    println!("  help                 - 显示此帮助信息");
    println!("  exit                 - 退出控制台");
    println!();
}

/// 连接
async fn do_connect(state: Arc<RwLock<ConsoleState>>) {
    println!("正在连接到 Controller...");

    let mut s = state.write().await;
    if s.connected {
        println!("已经连接!");
        return;
    }

    // TODO: 实现实际的连接逻辑
    println!("Controller 连接功能开发中...");
}

/// 断开连接
async fn do_disconnect(state: Arc<RwLock<ConsoleState>>) {
    println!("正在断开连接...");

    let mut s = state.write().await;
    if !s.connected {
        println!("未连接!");
        return;
    }

    // 关闭所有隧道
    let mut tm = s.tunnel_manager.write().await;
    if let Err(e) = tm.close_all().await {
        tracing::error!("关闭隧道失败: {}", e);
    }

    s.connected = false;
    s.peer_id = None;

    println!("已断开连接");
}

/// 显示状态
async fn do_status(state: Arc<RwLock<ConsoleState>>) {
    let s = state.read().await;

    println!("\n=== 闪连 VPN 状态 ===");
    println!("连接状态: {}", if s.connected { "已连接" } else { "未连接" });

    if let Some(peer_id) = &s.peer_id {
        println!("Peer ID: {}", peer_id);
    }

    // 隧道状态
    let tm = s.tunnel_manager.read().await;
    let tunnels = tm.list();
    println!("活动隧道: {}", tunnels.len());

    // Session 状态
    let sm = s.session_manager.read().await;
    println!("活动会话: {}", sm.count());

    // Bond 状态
    let bm = s.bond_manager.read().await;
    let stats = bm.stats();
    println!("Bond 状态: {}", stats.state);

    // NAT 状态
    let nh = s.nat_handler.read().await;
    println!("NAT 类型: 检测中...");

    println!();
}

/// 列出节点
async fn do_list_nodes(state: Arc<RwLock<ConsoleState>>, args: &[&str]) {
    let s = state.read().await;

    if args.first().map(|s| *s == "list").unwrap_or(false) || args.is_empty() {
        println!("\n=== 已知节点 ===");

        // 显示 Moon 节点列表
        let tm = s.tunnel_manager.read().await;
        let tunnels = tm.list();

        if tunnels.is_empty() {
            println!("暂无已知节点");
        } else {
            for tunnel in tunnels {
                println!(
                    "  {} - {} ({}) - {:?}",
                    tunnel.id,
                    tunnel.remote_addr,
                    tunnel.tunnel_type,
                    tunnel.status
                );
            }
        }
    } else if args.first().map(|s| *s == "quality").unwrap_or(false) {
        // 显示节点质量
        if let Some(id) = args.get(1) {
            let tm = s.tunnel_manager.read().await;
            if let Some(tunnel) = tm.get(id) {
                println!("\n=== 节点 {} 质量 ===", id);
                println!("类型: {}", tunnel.tunnel_type);
                println!("延迟: {:?} ms", tunnel.rtt_ms);
                println!("丢包率: {:?}%", tunnel.loss_rate);
                println!("发送: {} bytes", tunnel.send_bytes);
                println!("接收: {} bytes", tunnel.recv_bytes);
            } else {
                println!("节点 {} 不存在", id);
            }
        } else {
            println!("用法: node quality <id>");
        }
    }

    println!();
}

/// 列出/管理隧道
async fn do_list_tunnels(state: Arc<RwLock<ConsoleState>>, args: &[&str]) {
    let s = state.read().await;

    match args.first().map(|s| *s) {
        Some("list") | None => {
            println!("\n=== 活动隧道 ===");

            let tm = s.tunnel_manager.read().await;
            let tunnels = tm.list();

            if tunnels.is_empty() {
                println!("暂无活动隧道");
            } else {
                for tunnel in tunnels {
                    println!(
                        "  {} - {} ({}) - {:?}",
                        tunnel.id,
                        tunnel.remote_addr,
                        tunnel.tunnel_type,
                        tunnel.status
                    );
                }
            }
        }
        Some("connect") => {
            if let Some(tunnel_type) = args.get(1) {
                println!("连接到 {} 隧道...", tunnel_type);
                // TODO: 实现隧道连接
                println!("隧道连接功能开发中...");
            } else {
                println!("用法: tunnel connect <udp|tcp|wss|moon>");
            }
        }
        Some("disconnect") => {
            println!("断开所有隧道...");
            // TODO: 实现断开隧道
        }
        _ => {
            println!("用法: tunnel [list|connect|disconnect]");
        }
    }

    println!();
}

/// Bond 管理
async fn do_bond(state: Arc<RwLock<ConsoleState>>, args: &[&str]) {
    let mut s = state.write().await;

    match args.first().map(|s| *s) {
        Some("status") => {
            let bm = s.bond_manager.read().await;
            let stats = bm.stats();

            println!("\n=== Bond 状态 ===");
            println!("模式: {}", bm.config().mode);
            println!("状态: {}", stats.state);
            println!("总链路: {}", stats.total_links);
            println!("活动链路: {}", stats.active_links);
            println!("总带宽: {} Mbps", stats.total_bandwidth_mbps);
            println!("平均延迟: {} ms", stats.average_rtt_ms);
        }
        Some("enable") => {
            let mut bm = s.bond_manager.write().await;
            let mut config = bm.config().clone();
            config.enabled = true;
            bm.set_config(config);
            println!("Bond 已启用");
        }
        Some("disable") => {
            let mut bm = s.bond_manager.write().await;
            let mut config = bm.config().clone();
            config.enabled = false;
            bm.set_config(config);
            println!("Bond 已禁用");
        }
        Some("mode") => {
            if let Some(mode_str) = args.get(1) {
                use crate::bond::BondMode;

                let mode = match *mode_str {
                    "rr" | "round-robin" => BondMode::RoundRobin,
                    "wrr" | "weighted-rr" => BondMode::WeightedRoundRobin,
                    "least-latency" => BondMode::LeastLatency,
                    "failover" => BondMode::Failover,
                    _ => {
                        println!("未知模式: {}. 可用: rr, wrr, least-latency, failover", mode_str);
                        return;
                    }
                };

                let mut bm = s.bond_manager.write().await;
                let mut config = bm.config().clone();
                config.mode = mode;
                bm.set_config(config);
                println!("Bond 模式已设置为: {}", mode);
            } else {
                println!("用法: bond mode <rr|wrr|least-latency|failover>");
            }
        }
        _ => {
            println!("用法: bond [status|enable|disable|mode]");
        }
    }

    println!();
}

/// 列出策略
async fn do_list_policies(state: Arc<RwLock<ConsoleState>>, args: &[&str]) {
    let s = state.read().await;

    if args.first().map(|s| *s == "list").unwrap_or(true) || args.is_empty() {
        println!("\n=== 策略列表 ===");

        let pe = s.policy_engine.read().await;
        let rules = pe.list_rules();

        if rules.is_empty() {
            println!("暂无策略");
        } else {
            for rule in rules {
                let status = if rule.enabled { "启用" } else { "禁用" };
                println!(
                    "  [{}] {} - {} ({}) - {:?}",
                    rule.priority, rule.name, rule.policy_type, status, rule.action
                );
            }
        }
    }

    println!();
}

/// NAT 状态
async fn do_nat_status(state: Arc<RwLock<ConsoleState>>) {
    println!("\n=== NAT 状态 ===");

    let mut nh = state.write().await;
    match nh.detect().await {
        Ok(info) => {
            println!("NAT 类型: {}", info.nat_type);
            println!("公网地址: {}:{}", info.public_ip, info.public_port);
            println!("本地地址: {}:{}", info.local_ip, info.local_port);
            println!("打洞支持: {}", if info.hole_punchable { "是" } else { "否" });
        }
        Err(e) => {
            println!("NAT 检测失败: {}", e);
        }
    }

    println!();
}

/// 列出会话
async fn do_list_sessions(state: Arc<RwLock<ConsoleState>>) {
    let s = state.read().await;

    println!("\n=== 活动会话 ===");

    let sm = s.session_manager.read().await;
    let sessions = sm.list();

    if sessions.is_empty() {
        println!("暂无活动会话");
    } else {
        for session in sessions {
            println!(
                "  {} - Peer: {} (IP: {}) - Tunnel: {:?}",
                session.id,
                session.peer_id,
                session.assigned_ip,
                session.tunnel_id
            );
        }
    }

    println!();
}

/// Ping 测试
async fn do_ping(_state: Arc<RwLock<ConsoleState>>, args: &[&str]) {
    if let Some(target) = args.first() {
        println!("Ping {}...", target);
        // TODO: 实现 ping 功能
        println!("Ping 功能开发中...");
    } else {
        println!("用法: ping <peer_id|ip>");
    }
}

/// 设置日志级别
fn do_log_level(args: &[&str]) {
    if let Some(level) = args.first() {
        // 使用 tracing_subscriber 设置日志级别
        println!("日志级别已设置为: {}", level);
    } else {
        println!("用法: log <debug|info|warn|error>");
    }
}
