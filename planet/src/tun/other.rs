//! 其他平台 TUN 设备实现
//!
//! 用于不支持的平台 (如 FreeBSD, OpenBSD 等)

use anyhow::{Context, Result};

/// 打开 TUN 设备 (其他平台)
pub async fn open_tun(name: &str, mtu: u32) -> Result<std::os::fd::RawFd> {
    let device_path = if name.is_empty() {
        "/dev/tun0".to_string()
    } else {
        format!("/dev/{}", name)
    };

    let fd = std::fs::OpenOptions::new()
        .read(true)
        .write(true)
        .open(&device_path)
        .with_context(|| format!("打开 TUN 设备 {} 失败", device_path))?
        .into_raw_fd();

    tracing::info!("打开 TUN 设备 {} 成功 (MTU: {})", device_path, mtu);
    Ok(fd)
}

/// 设置 IP 地址
pub async fn set_ip(name: &str, ip: &str, netmask: &str) -> Result<()> {
    use std::process::Command;

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

    let output = Command::new("route")
        .args(["add", dest, gateway])
        .output()
        .context("添加路由失败")?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        if !stderr.contains("File exists") {
            return Err(anyhow::anyhow!("添加路由失败: {}", stderr));
        }
    }

    tracing::info!("添加路由 {} via {}", dest, gateway);
    Ok(())
}

/// 删除路由
pub async fn remove_route(dest: &str) -> Result<()> {
    use std::process::Command;

    let output = Command::new("route")
        .args(["delete", dest])
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
