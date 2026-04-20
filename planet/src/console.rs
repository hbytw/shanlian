//! 交互式控制台模块

use crate::Config;
use anyhow::Result;

/// 启动交互式控制台
pub async fn start_interactive(_config: Config) -> Result<()> {
    println!("闪连 VPN 控制台");
    println!("================");
    println!("输入 'help' 获取可用命令");
    println!("输入 'exit' 退出");

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

        match input {
            "exit" | "quit" | "q" => {
                println!("再见!");
                break;
            }
            "help" | "?" => {
                print_help();
            }
            "status" => {
                println!("状态: 未连接");
            }
            "connect" => {
                println!("正在连接...");
            }
            "disconnect" => {
                println!("已断开连接");
            }
            _ => {
                println!("未知命令: {}. 输入 'help' 获取帮助.", input);
            }
        }
    }

    Ok(())
}

fn print_help() {
    println!("\n可用命令:");
    println!("  connect     - 连接到 VPN 服务器");
    println!("  disconnect  - 断开 VPN 连接");
    println!("  status      - 显示当前状态");
    println!("  list nodes  - 列出已知节点");
    println!("  list tunnels - 列出活动隧道");
    println!("  nat status  - 显示 NAT 类型");
    println!("  ping        - 测试连接延迟");
    println!("  help        - 显示此帮助信息");
    println!("  exit        - 退出控制台");
    println!();
}
