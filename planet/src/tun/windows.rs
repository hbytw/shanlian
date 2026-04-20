//! Windows TUN 设备实现
//!
//! 使用 Windows TAP-Windows 驱动创建 TUN 设备

use anyhow::{Context, Result};
use std::os::fd::{AsRawFd, FromRawFd, IntoRawFd};
use std::process::Command;

/// 打开 TUN 设备 (Windows)
pub async fn open_tun(name: &str, mtu: u32) -> Result<std::os::fd::RawFd> {
    // Windows 使用 TAP-Windows 驱动
    // 设备名格式: \\.\Global\{GUID}.tap
    let device_path = if name.is_empty() || name == "planet0" {
        find_tap_device()?
    } else {
        // 使用指定的设备名
        format!("\\\\.\\Global\\{}.tap", name)
    };

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

/// 查找 TAP 设备 (通过注册表)
fn find_tap_device() -> Result<String> {
    // 尝试通过注册表查找 TAP 设备
    let output = Command::new("reg")
        .args([
            "query",
            r"HKLM\SYSTEM\CurrentControlSet\Control\Network\{4D36E972-E325-11CE-BFC1-08002BE10318}",
        ])
        .output();

    match output {
        Ok(o) if o.status.success() => {
            let stdout = String::from_utf8_lossy(&o.stdout);
            // 解析注册表输出，查找包含 "Connection" 的项
            for line in stdout.lines() {
                if line.contains("Connection") {
                    // 提取 GUID
                    let parts: Vec<&str> = line.trim().split_whitespace().collect();
                    if parts.len() >= 3 {
                        let guid = parts.last().unwrap_or(&"");
                        if !guid.is_empty() && !guid.contains("{") {
                            // 尝试查询这个连接的名称
                            let name_output = Command::new("reg")
                                .args([
                                    "query",
                                    &format!(r"HKLM\SYSTEM\CurrentControlSet\Control\Network\{4D36E972-E325-11CE-BFC1-08002BE10318}\{}\Connection", guid),
                                ])
                                .output();

                            if let Ok(no) = name_output {
                                let name_stdout = String::from_utf8_lossy(&no.stdout);
                                if name_stdout.contains("TAP") || name_stdout.contains("ShanLian") {
                                    return Ok(format!("\\\\.\\Global\\{}.tap", guid));
                                }
                            }
                        }
                    }
                }
            }
        }
        _ => {}
    }

    // 如果注册表查询失败，尝试默认的 TAP 适配器
    // 通过 ipconfig 查找
    let ipconfig_output = Command::new("ipconfig")
        .output()
        .context("执行 ipconfig 失败")?;

    let stdout = String::from_utf8_lossy(&ipconfig_output.stdout);

    // 查找 TAP-Windows 适配器
    let mut current_adapter = String::new();
    for line in stdout.lines() {
        let line = line.trim();
        if line.starts_with("adapter ") || line.starts_with("适配器 ") {
            current_adapter = line.replace("adapter ", "").replace("适配器 ", "").trim().to_string();
        } else if line.contains("TAP-Windows") || line.contains("ShanLian") {
            if !current_adapter.is_empty() {
                // 找到了 TAP 适配器，提取名称
                let clean_name = current_adapter.trim_end_matches(':').trim();
                tracing::info!("找到 TAP 适配器: {}", clean_name);
                return Ok(format!("\\\\.\\Global\\{}.tap", clean_name));
            }
        }
    }

    // 如果都找不到，返回一个常见的默认 GUID
    tracing::warn!("未找到 TAP 设备，使用默认 GUID");
    Ok("\\\\.\\Global\\{4F50731A-8992-4A2C-A4C2-A1E8D0D3D7AE}.tap".to_string())
}

/// 设置 MTU
async fn set_mtu(device_path: &str, mtu: u32) -> Result<()> {
    // 从设备路径提取适配器名称
    let adapter_name = device_path
        .trim_start_matches("\\\\.\\Global\\")
        .trim_end_matches(".tap");

    // 提取 GUID 部分
    let guid = if adapter_name.contains("{") {
        adapter_name.to_string()
    } else {
        // 如果不是 GUID，尝试通过注册表查找对应的 GUID
        find_adapter_guid(adapter_name)?
    };

    let output = Command::new("netsh")
        .args([
            "interface",
            "ipv4",
            "set",
            "subinterface",
            &guid,
            "mtu",
            &mtu.to_string(),
        ])
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

/// 查找适配器对应的 GUID
fn find_adapter_guid(adapter_name: &str) -> Result<String> {
    // 遍历注册表查找对应的 GUID
    let output = Command::new("reg")
        .args([
            "query",
            r"HKLM\SYSTEM\CurrentControlSet\Control\Network\{4D36E972-E325-11CE-BFC1-08002BE10318}",
        ])
        .output()
        .context("查询网络适配器注册表失败")?;

    let stdout = String::from_utf8_lossy(&output.stdout);

    for line in stdout.lines() {
        let line = line.trim();
        if line.starts_with("HKEY_") || line.starts_with("HKLM\\") {
            // 提取 GUID
            if let Some(guid) = line.split_whitespace().last() {
                let guid = guid.trim();
                if guid.starts_with("{") && guid.ends_with("}") {
                    // 查询这个 GUID 对应的名称
                    let name_output = Command::new("reg")
                        .args([
                            "query",
                            &format!(r"HKLM\SYSTEM\CurrentControlSet\Control\Network\{4D36E972-E325-11CE-BFC1-08002BE10318}\{}", guid),
                        ])
                        .output();

                    if let Ok(no) = name_output {
                        let name_stdout = String::from_utf8_lossy(&no.stdout);
                        if name_stdout.contains(adapter_name) || name_stdout.contains("TAP") {
                            return Ok(guid.to_string());
                        }
                    }
                }
            }
        }
    }

    // 如果找不到，返回传入的名称
    Ok(adapter_name.to_string())
}

/// 设置 IP 地址
pub async fn set_ip(name: &str, ip: &str, netmask: &str) -> Result<()> {
    // 计算 CIDR
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
        // 如果设备名不是正确的接口名，尝试查找并使用 GUID
        if stderr.contains("参数不正确") || stderr.contains("not find") {
            tracing::warn!("设备 {} 不正确，尝试使用 TAP GUID", name);
            return set_ip_by_guid(name, ip, cidr).await;
        }
        return Err(anyhow::anyhow!("设置 IP 地址失败: {}", stderr));
    }

    tracing::info!("设置 IP 地址 {}/{} -> {}", ip, netmask, name);
    Ok(())
}

/// 通过 GUID 设置 IP
async fn set_ip_by_guid(guid_or_name: &str, ip: &str, cidr: u32) -> Result<()> {
    // 查找对应的 TAP 设备 GUID
    let device_path = find_tap_device()?;
    let guid = device_path
        .trim_start_matches("\\\\.\\Global\\")
        .trim_end_matches(".tap");

    let output = Command::new("netsh")
        .args([
            "interface", "ipv4", "set", "address",
            guid, "static", ip, &cidr.to_string()
        ])
        .output()
        .context("通过 GUID 设置 IP 地址失败")?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        return Err(anyhow::anyhow!("通过 GUID 设置 IP 地址失败: {}", stderr));
    }

    tracing::info!("通过 GUID {} 设置 IP 地址成功: {}/{}", guid, ip, cidr);
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
