//! TUN 虚拟网卡模块
//!
//! 负责在 Linux/Windows/macOS 上创建和管理 TUN 虚拟网卡

use anyhow::{Context, Result};
use std::os::fd::{AsRawFd, FromRawFd, IntoRawFd};
use std::path::PathBuf;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::sync::mpsc;
use tokio::net::unix::AsyncFd;

#[cfg(target_os = "linux")]
mod linux;

#[cfg(target_os = "windows")]
mod windows;

#[cfg(target_os = "macos")]
mod darwin;

#[cfg(not(any(target_os = "linux", target_os = "windows", target_os = "macos")))]
mod other;

/// TUN 设备句柄
pub struct TunDevice {
    /// 文件描述符 (用于异步 I/O)
    fd: AsyncFd<std::os::fd::RawFd>,
    /// 设备名称 (如 "tun0", "planet0")
    name: String,
    /// MTU 大小
    mtu: u32,
}

impl TunDevice {
    /// 创建新的 TUN 设备
    pub async fn new(name: &str, mtu: u32) -> Result<Self> {
        let raw_fd = Self::open(name, mtu).await?;

        // 将 RawFd 包装为 AsyncFd
        let fd = AsyncFd::new(raw_fd)
            .context("创建 AsyncFd 失败")?;

        let dev = Self {
            fd,
            name: name.to_string(),
            mtu,
        };

        tracing::info!("TUN 设备 {} 创建成功 (MTU: {})", dev.name, dev.mtu);
        Ok(dev)
    }

    /// 打开 TUN 设备 (平台特定实现)
    async fn open(name: &str, mtu: u32) -> Result<std::os::fd::RawFd> {
        #[cfg(target_os = "linux")]
        {
            linux::open_tun(name, mtu).await
        }

        #[cfg(target_os = "windows")]
        {
            windows::open_tun(name, mtu).await
        }

        #[cfg(target_os = "macos")]
        {
            darwin::open_tun(name, mtu).await
        }

        #[cfg(not(any(target_os = "linux", target_os = "windows", target_os = "macos")))]
        {
            other::open_tun(name, mtu).await
        }
    }

    /// 获取设备名称
    pub fn name(&self) -> &str {
        &self.name
    }

    /// 获取 MTU
    pub fn mtu(&self) -> u32 {
        self.mtu
    }

    /// 设置 IP 地址
    pub async fn set_ip(&self, ip: &str, netmask: &str) -> Result<()> {
        #[cfg(target_os = "linux")]
        {
            linux::set_ip(&self.name, ip, netmask).await
        }

        #[cfg(target_os = "windows")]
        {
            windows::set_ip(&self.name, ip, netmask).await
        }

        #[cfg(target_os = "macos")]
        {
            darwin::set_ip(&self.name, ip, netmask).await
        }

        #[cfg(not(any(target_os = "linux", target_os = "windows", target_os = "macos")))]
        {
            other::set_ip(&self.name, ip, netmask).await
        }
    }

    /// 启动 TUN 设备
    pub async fn up(&self) -> Result<()> {
        #[cfg(target_os = "linux")]
        {
            linux::set_up(&self.name).await
        }

        #[cfg(target_os = "windows")]
        {
            windows::set_up(&self.name).await
        }

        #[cfg(target_os = "macos")]
        {
            darwin::set_up(&self.name).await
        }

        #[cfg(not(any(target_os = "linux", target_os = "windows", target_os = "macos")))]
        {
            other::set_up(&self.name).await
        }
    }

    /// 停止 TUN 设备
    pub async fn down(&self) -> Result<()> {
        #[cfg(target_os = "linux")]
        {
            linux::set_down(&self.name).await
        }

        #[cfg(target_os = "windows")]
        {
            windows::set_down(&self.name).await
        }

        #[cfg(target_os = "macos")]
        {
            darwin::set_down(&self.name).await
        }

        #[cfg(not(any(target_os = "linux", target_os = "windows", target_os = "macos")))]
        {
            other::set_down(&self.name).await
        }
    }

    /// 添加路由
    pub async fn add_route(&self, dest: &str, gateway: &str) -> Result<()> {
        #[cfg(target_os = "linux")]
        {
            linux::add_route(dest, gateway).await
        }

        #[cfg(target_os = "windows")]
        {
            windows::add_route(dest, gateway).await
        }

        #[cfg(target_os = "macos")]
        {
            darwin::add_route(dest, gateway).await
        }

        #[cfg(not(any(target_os = "linux", target_os = "windows", target_os = "macos")))]
        {
            other::add_route(dest, gateway).await
        }
    }

    /// 删除路由
    pub async fn remove_route(&self, dest: &str) -> Result<()> {
        #[cfg(target_os = "linux")]
        {
            linux::remove_route(dest).await
        }

        #[cfg(target_os = "windows")]
        {
            windows::remove_route(dest).await
        }

        #[cfg(target_os = "macos")]
        {
            darwin::remove_route(dest).await
        }

        #[cfg(not(any(target_os = "linux", target_os = "windows", target_os = "macos")))]
        {
            other::remove_route(dest).await
        }
    }

    /// 设置默认路由
    pub async fn set_default_route(&self, gateway: &str) -> Result<()> {
        self.add_route("0.0.0.0/0", gateway).await
    }

    /// 异步读取 IP 包
    pub async fn read_packet(&self, buf: &mut [u8]) -> Result<usize> {
        loop {
            let mut guard = self.fd.readable().await?;

            match guard.try_io(|fd| {
                // SAFETY: fd is valid and we own it
                let fd = fd.as_raw_fd();
                let ret = unsafe {
                    libc::read(fd, buf.as_mut_ptr() as *mut libc::c_void, buf.len())
                };
                if ret < 0 {
                    Err(std::io::Error::last_os_error())
                } else {
                    Ok(ret as usize)
                }
            }) {
                Ok(result) => return result,
                Err(_) => continue,
            }
        }
    }

    /// 异步写入 IP 包
    pub async fn write_packet(&self, buf: &[u8]) -> Result<usize> {
        loop {
            let mut guard = self.fd.writable().await?;

            match guard.try_io(|fd| {
                // SAFETY: fd is valid and we own it
                let fd = fd.as_raw_fd();
                let ret = unsafe {
                    libc::write(fd, buf.as_ptr() as *const libc::c_void, buf.len())
                };
                if ret < 0 {
                    Err(std::io::Error::last_os_error())
                } else {
                    Ok(ret as usize)
                }
            }) {
                Ok(result) => return result,
                Err(_) => continue,
            }
        }
    }

    /// 关闭设备
    pub async fn close(self) -> Result<()> {
        tracing::info!("关闭 TUN 设备 {}", self.name);

        // 从 AsyncFd 获取 RawFd 并关闭
        let raw_fd = self.fd.into_inner();
        unsafe {
            libc::close(raw_fd);
        }

        tracing::info!("TUN 设备 {} 已关闭", self.name);
        Ok(())
    }
}

impl Drop for TunDevice {
    fn drop(&mut self) {
        tracing::debug!("TUN 设备 {} 被 drop", self.name);
    }
}

/// TUN 设备配置
#[derive(Debug, Clone)]
pub struct TunConfig {
    /// 设备名称
    pub name: String,
    /// MTU
    pub mtu: u32,
    /// IP 地址
    pub ip: String,
    /// 子网掩码
    pub netmask: String,
    /// 默认网关
    pub gateway: String,
}

impl Default for TunConfig {
    fn default() -> Self {
        Self {
            name: "planet0".to_string(),
            mtu: 1400,
            ip: "10.8.0.2".to_string(),
            netmask: "255.255.255.0".to_string(),
            gateway: "10.8.0.1".to_string(),
        }
    }
}

impl TunConfig {
    /// 从配置文件加载
    pub fn load_from_file(path: &PathBuf) -> Result<Self> {
        let content = std::fs::read_to_string(path)
            .with_context(|| format!("读取配置文件 {} 失败", path.display()))?;
        let config: TunConfig = serde_yaml::from_str(&content)?;
        Ok(config)
    }

    /// 保存到配置文件
    pub fn save_to_file(&self, path: &PathBuf) -> Result<()> {
        let content = serde_yaml::to_string(self)?;
        std::fs::write(path, content)?;
        Ok(())
    }
}
