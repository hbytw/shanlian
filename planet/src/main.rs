//! 闪连 VPN Planet 客户端
//!
//! Planet 是闪连 VPN 的客户端程序，负责：
//! - 连接 Controller 进行设备注册和认证
//! - 建立到 Moon 或其他 Planet 的隧道
//! - 管理 TUN 虚拟网卡
//! - 执行路由和策略

use clap::Parser;
use std::path::PathBuf;
use tracing_subscriber::{layer::SubscriberExt, util::SubscriberInitExt};

mod cli;
mod config;
mod console;
mod controller;
mod crypto;
mod tunnel;
mod nat;
mod routing;
mod session;

pub use config::Config;

#[derive(Parser, Debug)]
#[command(name = "planet")]
#[command(about = "闪连 VPN Planet 客户端", long_about = None)]
struct Args {
    /// Controller 服务器地址
    #[arg(long, default_value = "https://controller.ytw.fr:8443")]
    controller: String,

    /// 运行模式
    #[arg(short, long, default_value = "connect")]
    mode: String,

    /// 配置文件路径
    #[arg(short, long)]
    config: Option<PathBuf>,

    /// 日志级别
    #[arg(long, default_value = "info")]
    log_level: String,

    /// 输出格式 (text/json)
    #[arg(long, default_value = "text")]
    output: String,

    /// 控制台交互模式
    #[arg(long, short)]
    console: bool,

    /// 显示版本信息
    #[arg(long, short)]
    version: bool,
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let args = Args::parse();

    // 初始化日志
    let env_filter = tracing_subscriber::EnvFilter::try_from_default_env()
        .unwrap_or_else(|_| args.log_level.clone().into());

    let fmt_layer = tracing_subscriber::fmt::layer()
        .with_target(true)
        .with_thread_ids(true);

    tracing_subscriber::registry()
        .with(env_filter)
        .with(fmt_layer)
        .init();

    tracing::info!("闪连 VPN Planet 客户端启动中...");
    tracing::info!("版本: {}", env!("CARGO_PKG_VERSION"));

    // 版本信息
    if args.version {
        println!("闪连 VPN Planet 客户端");
        println!("版本: {}", env!("CARGO_PKG_VERSION"));
        println!("构建时间: {}", chrono::Local::now().format("%Y-%m-%d %H:%M:%S"));
        return Ok(());
    }

    // 加载配置
    let config = Config::load(args.config.clone())?;

    // 根据模式运行
    match args.mode.as_str() {
        "console" | "interactive" => {
            console::start_interactive(config).await?;
        }
        "connect" | _ => {
            planet::run(config).await?;
        }
        "disconnect" => {
            planet::disconnect().await?;
        }
        "status" => {
            planet::show_status().await?;
        }
    }

    Ok(())
}

mod planet {
    use super::*;

    pub async fn run(config: Config) -> anyhow::Result<()> {
        tracing::info!("正在连接到 Controller: {}", config.controller);

        // TODO: 实现连接逻辑
        tracing::info!("连接功能开发中...");
        Ok(())
    }

    pub async fn disconnect() -> anyhow::Result<()> {
        // TODO: 实现断开连接
        tracing::info!("断开连接功能开发中...");
        Ok(())
    }

    pub async fn show_status() -> anyhow::Result<()> {
        // TODO: 显示状态
        println!("状态功能开发中...");
        Ok(())
    }
}
