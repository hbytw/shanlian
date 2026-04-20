//! macOS TUN 设备实现
//!
//! 使用 utun 接口创建 TUN 设备

use anyhow::{Context, Result};
use std::os::fd::{AsRawFd, FromRawFd, IntoRawFd};

/// 打开 TUN 设备 (macOS)
pub async fn open_tun(name: &str, mtu: u32) -> Result<std::os::fd::RawFd> {
    // macOS 使用 utun 接口
    // 查找可用的 utun 设备
    let utun_name = if name.is_empty() || name == "planet0" {
        find_available_utun()?
    } else {
        name.to_string()
    };

    // 打开 utun 设备
    let fd = std::fs::OpenOptions::new()
        .read(true)
        .write(true)
        .open(format!("/dev/{}", utun_name))
        .with_context(|| format!("打开 utun 设备 /dev/{} 失败", utun_name))?
        .into_raw_fd();

    // 设置 MTU
    set_mtu(&utun_name, mtu).await?;

    tracing::info!("打开 utun 设备 {} 成功 (MTU: {})", utun_name, mtu);
    Ok(fd)
}

/// 查找可用的 utun 设备
fn find_available_utun() -> Result<String> {
    use std::process::Command;

    // 使用 ifconfig 列出 utun 设备
    let output = Command::new("ifconfig")
        .args(["-l", "utun"])
        .output()
        .context("列出 utun 设备失败")?;

    let stdout = String::from_utf8_lossy(&output.stdout);

    // 查找最大的 utun 编号
    let mut max_num = -1i32;
    for line in stdout.split_whitespace() {
        if line.starts_with("utun") {
            if let Ok(n) = line[4..].parse::<i32>() {
                max_num = max_num.max(n);
            }
        }
    }

    let next_num = max_num + 1;
    Ok(format!("utun{}", next_num))
}

/// 设置 MTU
async fn set_mtu(name: &str, mtu: u32) -> Result<()> {
    use std::process::Command;

    let output = Command::new("ifconfig")
        .args([name, "mtu", &mtu.to_string()])
        .output()
        .context("设置 MTU 失败")?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        tracing::warn!("设置 MTU 警告: {}", stderr);
    }

    Ok(())
}

/// 设置 IP 地址
pub async fn set_ip(name: &str, ip: &str, netmask: &str) -> Result<()> {
    use std::process::Command;

    // 计算网络地址
    let cidr = netmask_to_cidr(netmask);

    let output = Command::new("ifconfig")
        .args([name, ip, &format!("/{}/{}", ip, cidr)])
        .output()
        .context("设置 IP 地址失败")?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        return Err(anyhow::anyhow!("设置 IP 地址失败: {}", stderr));
    }

    tracing::info!("设置 IP 地址 {}/{} -> {}", ip, netmask, name);
    Ok(())
}

/// 将子网掩码转换为 CIDR
fn netmask_to_cidr(netmask: &str) -> u8 {
    let parts: Vec<&str> = netmask.split('.').collect();
    if parts.len() != 4 {
        return 24;
    }

    let mut cidr: u8 = 0;
    for part in parts {
        if let Ok(n) = part.parse::<u8>() {
            cidr += n.count_ones() as u8;
        }
    }
    cidr
}

/// 设置设备 UP
pub async fn set_up(name: &str) -> Result<()> {
    use std::process::Command;

    let output = Command::new("ifconfig")
        .args([name, "up"])
        .output()
        .context("设置设备 UP 失败")?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        return Err(anyhow::anyhow!("设置设备 UP 失败: {}", stderr));
    }

    tracing::info!("设备 {} 已 UP", name);
    Ok(())
}

/// 设置设备 DOWN
pub async fn set_down(name: &str) -> Result<()> {
    use std::process::Command;

    let output = Command::new("ifconfig")
        .args([name, "down"])
        .output()
        .context("设置设备 DOWN 失败")?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        return Err(anyhow::anyhow!("设置设备 DOWN 失败: {}", stderr));
    }

    tracing::info!("设备 {} 已 DOWN", name);
    Ok(())
}

/// 添加路由
pub async fn add_route(dest: &str, gateway: &str) -> Result<()> {
    use std::process::Command;

    // 解析目标网络
    let (network, cidr) = parse_destination(dest)?;

    let output = if gateway.is_empty() || gateway == "0.0.0.0" {
        Command::new("route")
            .args(["add", "-net", &network, "-netmask", &cidr_to_netmask(cidr), "-interface", &network])
            .output()?
    } else {
        Command::new("route")
            .args(["add", "-net", &network, "-netmask", &cidr_to_netmask(cidr), gateway])
            .output()?
    };

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        if !stderr.contains("File exists") {
            return Err(anyhow::anyhow!("添加路由失败: {}", stderr));
        }
    }

    tracing::info!("添加路由 {} via {}", dest, gateway);
    Ok(())
}

/// 解析目标网络
fn parse_destination(dest: &str) -> Result<(String, u32)> {
    let parts: Vec<&str> = dest.split('/').collect();
    if parts.len() == 2 {
        let cidr = parts[1].parse::<u32>().unwrap_or(24);
        Ok((parts[0].to_string(), cidr))
    } else {
        Ok((dest.to_string(), 24))
    }
}

/// CIDR 转网络掩码
fn cidr_to_netmask(cidr: u32) -> String {
    let mask = !0u32 << (32 - cidr);
    format!(
        "{}.{}.{}.{}",
        (mask >> 24) as u8,
        (mask >> 16) as u8,
        (mask >> 8) as u8,
        mask as u8
    )
}

/// 删除路由
pub async fn remove_route(dest: &str) -> Result<()> {
    use std::process::Command;

    let (network, cidr) = parse_destination(dest)?;

    let output = Command::new("route")
        .args(["delete", "-net", &network, "-netmask", &cidr_to_netmask(cidr)])
        .output()
        .context("删除路由失败")?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        if !stderr.contains("not found") {
            return Err(anyhow::anyhow!("删除路由失败: {}", stderr));
        }
    }

    tracing::info!("删除路由 {}", dest);
    Ok(())
}
