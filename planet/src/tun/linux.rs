//! Linux TUN 设备实现
//!
//! 使用 /dev/net/tun 创建 TUN 设备

use anyhow::{Context, Result};
use std::os::fd::{AsRawFd, FromRawFd, IntoRawFd};
use tokio::io::{AsyncReadExt, AsyncWriteExt};

/// 打开 TUN 设备
pub async fn open_tun(name: &str, mtu: u32) -> Result<std::os::fd::RawFd> {
    // 打开 /dev/net/tun
    let fd = std::fs::OpenOptions::new()
        .read(true)
        .write(true)
        .open("/dev/net/tun")
        .context("打开 /dev/net/tun 失败")?
        .into_raw_fd();

    // 设置 TUN 设备属性
    let tun_name = if name.is_empty() {
        "planet%d".to_string()
    } else {
        name.to_string()
    };

    // 使用 ioctl 设置 TUN 设备
    #[repr(C)]
    struct Ifreq {
        name: [u8; 16],
        flags: u16,
    }

    let mut ifreq = Ifreq {
        name: [0u8; 16],
        flags: 0x0001 | 0x1000, // IFF_TUN | IFF_NO_PI
    };

    // 复制设备名称
    let name_bytes = tun_name.as_bytes();
    let len = name_bytes.len().min(15);
    ifreq.name[..len].copy_from_slice(&name_bytes[..len]);

    // 执行 ioctl
    let ret = unsafe {
        libc::ioctl(
            fd,
            libc::TUNSETIFF,
            &ifreq as *const _ as *const libc::c_void,
        )
    };

    if ret < 0 {
        return Err(anyhow::anyhow!("ioctl TUNSETIFF 失败: {}", ret));
    }

    // 设置 MTU
    set_mtu(name, mtu).await?;

    tracing::info!("打开 TUN 设备 {} 成功 (MTU: {})", name, mtu);
    Ok(fd)
}

/// 设置 MTU
async fn set_mtu(name: &str, mtu: u32) -> Result<()> {
    use std::process::Command;

    let output = Command::new("ip")
        .args(["link", "set", name, "mtu", &mtu.to_string()])
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
    let addr_with_prefix = format!("{}/{}", ip, cidr);

    let output = Command::new("ip")
        .args(["addr", "add", &addr_with_prefix, "dev", name])
        .output()
        .context("设置 IP 地址失败")?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        // 如果地址已存在，不视为错误
        if !stderr.contains("File exists") {
            return Err(anyhow::anyhow!("设置 IP 地址失败: {}", stderr));
        }
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

    let output = Command::new("ip")
        .args(["link", "set", name, "up"])
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

    let output = Command::new("ip")
        .args(["link", "set", name, "down"])
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

    let output = if gateway.is_empty() || gateway == "0.0.0.0" {
        Command::new("ip")
            .args(["route", "add", dest, "dev", "planet0"])
            .output()?
    } else {
        Command::new("ip")
            .args(["route", "add", dest, "via", gateway])
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

/// 删除路由
pub async fn remove_route(dest: &str) -> Result<()> {
    use std::process::Command;

    let output = Command::new("ip")
        .args(["route", "del", dest])
        .output()
        .context("删除路由失败")?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        // 如果路由不存在，不视为错误
        if !stderr.contains("No such file or directory") {
            return Err(anyhow::anyhow!("删除路由失败: {}", stderr));
        }
    }

    tracing::info!("删除路由 {}", dest);
    Ok(())
}
