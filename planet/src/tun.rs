//! TUN 虚拟网卡模块
//!
//! 负责在 Linux/Windows/macOS 上创建和管理 TUN 虚拟网卡

use anyhow::{Context, Result};
use std::path::PathBuf;

#[cfg(target_os = "linux")]
mod linux;

#[cfg(target_os = "windows")]
mod windows;

#[cfg(target_os = "macos")]
mod darwin;

#[cfg(not(any(target_os = "linux", target_os = "windows", target_os = "macos")))]
mod other;

/// TUN 设备句柄 - 跨平台抽象
#[cfg(unix)]
pub struct TunDevice {
    fd: std::os::fd::RawFd,
    name: String,
    mtu: u32,
}

/// Windows TUN 设备句柄
#[cfg(windows)]
pub struct TunDevice {
    handle: windows::TapHandle,
    name: String,
    mtu: u32,
}

/// 非 Unix 非 Windows 平台
#[cfg(not(any(unix, windows)))]
pub struct TunDevice {
    fd: i32,
    name: String,
    mtu: u32,
}

impl TunDevice {
    /// 创建新的 TUN 设备
    pub async fn new(name: &str, mtu: u32) -> Result<Self> {
        #[cfg(target_os = "linux")]
        {
            let fd = linux::open_tun(name, mtu).await?;
            tracing::info!("TUN 设备 {} 创建成功 (MTU: {})", name, mtu);
            Ok(Self { fd, name: name.to_string(), mtu })
        }

        #[cfg(target_os = "windows")]
        {
            let handle = windows::open_tun(name, mtu).await?;
            tracing::info!("TUN 设备 {} 创建成功 (MTU: {})", name, mtu);
            Ok(Self { handle, name: name.to_string(), mtu })
        }

        #[cfg(not(any(target_os = "linux", target_os = "windows", target_os = "macos")))]
        {
            let fd = other::open_tun(name, mtu).await?;
            tracing::info!("TUN 设备 {} 创建成功 (MTU: {})", name, mtu);
            Ok(Self { fd, name: name.to_string(), mtu })
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

    /// 异步读取 IP 包 (Unix)
    #[cfg(target_os = "linux")]
    pub async fn read_packet(&self, buf: &mut [u8]) -> Result<usize> {
        use std::os::fd::AsRawFd;
        let fd = self.fd;
        loop {
            let ret = unsafe {
                libc::read(fd, buf.as_mut_ptr() as *mut libc::c_void, buf.len())
            };
            if ret < 0 {
                let err = std::io::Error::last_os_error();
                if err.kind() == std::io::ErrorKind::WouldBlock {
                    tokio::time::sleep(std::time::Duration::from_millis(1)).await;
                    continue;
                }
                return Err(err.into());
            }
            return Ok(ret as usize);
        }
    }

    /// 异步写入 IP 包 (Unix)
    #[cfg(target_os = "linux")]
    pub async fn write_packet(&self, buf: &[u8]) -> Result<usize> {
        use std::os::fd::AsRawFd;
        let fd = self.fd;
        let ret = unsafe {
            libc::write(fd, buf.as_ptr() as *const libc::c_void, buf.len())
        };
        if ret < 0 {
            return Err(std::io::Error::last_os_error().into());
        }
        Ok(ret as usize)
    }

    /// 异步读取 IP 包 (macOS)
    #[cfg(target_os = "macos")]
    pub async fn read_packet(&self, buf: &mut [u8]) -> Result<usize> {
        use std::os::fd::AsRawFd;
        let fd = self.fd;
        let ret = unsafe {
            libc::read(fd, buf.as_mut_ptr() as *mut libc::c_void, buf.len())
        };
        if ret < 0 {
            return Err(std::io::Error::last_os_error().into());
        }
        Ok(ret as usize)
    }

    /// 异步写入 IP 包 (macOS)
    #[cfg(target_os = "macos")]
    pub async fn write_packet(&self, buf: &[u8]) -> Result<usize> {
        use std::os::fd::AsRawFd;
        let fd = self.fd;
        let ret = unsafe {
            libc::write(fd, buf.as_ptr() as *const libc::c_void, buf.len())
        };
        if ret < 0 {
            return Err(std::io::Error::last_os_error().into());
        }
        Ok(ret as usize)
    }

    /// 异步读取 IP 包 (Windows)
    #[cfg(windows)]
    pub async fn read_packet(&self, buf: &mut [u8]) -> Result<usize> {
        windows::read_packet(&self.handle, buf).await
    }

    /// 异步写入 IP 包 (Windows)
    #[cfg(windows)]
    pub async fn write_packet(&self, buf: &[u8]) -> Result<usize> {
        windows::write_packet(&self.handle, buf).await
    }

    /// 异步读取 IP 包 (其他平台 - 存根实现)
    #[cfg(not(any(target_os = "linux", target_os = "windows", target_os = "macos")))]
    pub async fn read_packet(&self, _buf: &mut [u8]) -> Result<usize> {
        anyhow::bail!("此平台 TUN read_packet 暂未实现")
    }

    /// 异步写入 IP 包 (其他平台 - 存根实现)
    #[cfg(not(any(target_os = "linux", target_os = "windows", target_os = "macos")))]
    pub async fn write_packet(&self, _buf: &[u8]) -> Result<usize> {
        anyhow::bail!("此平台 TUN write_packet 暂未实现")
    }

    /// 关闭设备
    #[cfg(target_os = "linux")]
    pub async fn close(self) -> Result<()> {
        let name = self.name.clone();
        tracing::info!("关闭 TUN 设备 {}", name);
        unsafe {
            libc::close(self.fd);
        }
        tracing::info!("TUN 设备 {} 已关闭", name);
        Ok(())
    }

    /// 关闭设备 (macOS)
    #[cfg(target_os = "macos")]
    pub async fn close(self) -> Result<()> {
        let name = self.name.clone();
        tracing::info!("关闭 TUN 设备 {}", name);
        unsafe {
            libc::close(self.fd);
        }
        tracing::info!("TUN 设备 {} 已关闭", name);
        Ok(())
    }

    /// 关闭设备 (Windows)
    #[cfg(windows)]
    pub async fn close(self) -> Result<()> {
        windows::close_tap(self.handle).await?;
        tracing::info!("TUN 设备 {} 已关闭", self.name);
        Ok(())
    }

    /// 关闭设备 (其他平台 - 存根)
    #[cfg(not(any(target_os = "linux", target_os = "windows", target_os = "macos")))]
    pub async fn close(self) -> Result<()> {
        let name = self.name.clone();
        tracing::info!("关闭 TUN 设备 {}", name);
        Ok(())
    }
}

/// TUN 设备配置
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
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
