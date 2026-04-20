//! Windows TUN 设备实现
//!
//! 使用 Windows TAP-Windows 驱动创建 TUN 设备

use anyhow::{Context, Result};
use std::os::fd::{AsRawFd, FromRawFd, IntoRawFd};

/// 打开 TUN 设备 (Windows)
pub async fn open_tun(name: &str, mtu: u32) -> Result<std::os::fd::RawFd> {
    // Windows 使用 TAP-Windows 驱动
    // 设备名格式: \\.\Global\{GUID}.tap
    let device_path = find_tap_device()?;

    let fd = std::fs::OpenOptions::new()
        .read(true)
        .write(true)
        .open(&device_path)
        .with_context(|| format!("打开 TAP 设备 {} 失败", device_path))?
        .into_raw_fd();

    // 设置 MTU
    set_mtu(&device_path, mtu).await?;

    tracing::info!("打开 TAP 设备 {} 成功 (MTU: {})", device_path, mtu);
    Ok(fd)
}

/// 查找 TAP 设备
fn find_tap_device() -> Result<String> {
    use std::process::Command;

    // 使用 ipconfig 查找 TAP 设备
    let output = Command::new("ipconfig")
        .output()
        .context("执行 ipconfig 失败")?;

    let stdout = String::from_utf8_lossy(&output.stdout);

    // 查找 "TAP-Windows" 适配器
    for line in stdout.lines() {
        if line.contains("TAP-Windows") {
            // 查找适配器名称
            if let Some(start) = stdout.find("Adapter ") {
                let end = stdout[start..].find('\r').unwrap_or(0);
                let adapter = stdout[start + 8..start + end].trim();
                return Ok(format!("\\\\.\\Global\\{}.tap", adapter));
            }
        }
    }

    // 如果找不到，返回默认设备路径
    // 实际使用时需要通过注册表查找真实 GUID
    Ok("\\\\.\\Global\\{4F50731A-8992-4A2C-A4C2-A1E8D0D3D7AE}.tap".to_string())
}

/// 设置 MTU
async fn set_mtu(_device_path: &str, mtu: u32) -> Result<()> {
    use std::process::Command;

    // 使用 netsh 设置 MTU
    let output = Command::new("netsh")
        .args(["interface", "ipv4", "set", "subinterface",
               "民间改", "mtu", &mtu.to_string()])
        .output();

    match output {
        Ok(o) if o.status.success() => {
            tracing::info!("设置 MTU 成功: {}", mtu);
        }
        Ok(o) => {
            let stderr = String::from_utf8_lossy(&o.stderr);
            tracing::warn!("设置 MTU 失败 (非致命): {}", stderr);
        }
        Err(e) => {
            tracing::warn!("设置 MTU 失败 (非致命): {}", e);
        }
    }

    Ok(())
}

/// 设置 IP 地址
pub async fn set_ip(name: &str, ip: &str, netmask: &str) -> Result<()> {
    use std::process::Command;

    // 计算网络地址
    let cidr = netmask_to_cidr(netmask);

    // 使用 netsh 设置 IP
    let output = Command::new("netsh")
        .args([
            "interface", "ipv4", "set", "address",
            name, "static", ip, &cidr.to_string()
        ])
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
fn netmask_to_cidr(netmask: &str) -> u32 {
    let parts: Vec<&str> = netmask.split('.').collect();
    if parts.len() != 4 {
        return 24;
    }

    let mut cidr: u32 = 0;
    for part in parts {
        if let Ok(n) = part.parse::<u8>() {
            cidr = (cidr << 8) | (n as u32);
        }
    }
    // 计算前导 1 的个数
    cidr.count_ones()
}

/// 设置设备 UP
pub async fn set_up(name: &str) -> Result<()> {
    use std::process::Command;

    let output = Command::new("netsh")
        .args(["interface", "set", "interface", name, "admin=enabled"])
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

    let output = Command::new("netsh")
        .args(["interface", "set", "interface", name, "admin=disabled"])
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
    let gateway = if gateway.is_empty() || gateway == "0.0.0.0" {
        "0.0.0.0"
    } else {
        gateway
    };

    let output = Command::new("route")
        .args(["add", &network, "mask", &cidr.to_string(), gateway])
        .output()
        .context("添加路由失败")?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        if !stderr.contains("object exists") {
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

/// 删除路由
pub async fn remove_route(dest: &str) -> Result<()> {
    use std::process::Command;

    let (network, cidr) = parse_destination(dest)?;

    let output = Command::new("route")
        .args(["delete", &network, "mask", &cidr.to_string()])
        .output()
        .context("删除路由失败")?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        if !stderr.contains("The system cannot find") {
            return Err(anyhow::anyhow!("删除路由失败: {}", stderr));
        }
    }

    tracing::info!("删除路由 {}", dest);
    Ok(())
}
