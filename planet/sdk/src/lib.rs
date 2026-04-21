//! ShanLian VPN C SDK
//!
//! 提供 C 语言的 FFI 接口

use std::collections::HashMap;
use std::ffi::{c_char, CStr, CString};
use std::os::raw::c_int;
use std::ptr;
use std::sync::{Arc, LazyLock, Mutex};

use tokio::runtime::Runtime;

// ============================================================================
// 类型定义
// ============================================================================

pub type ShanLianHandle = isize;

/// SDK 全局运行时
static SDK_RUNTIME: LazyLock<Mutex<Option<Runtime>>> = LazyLock::new(|| Mutex::new(None));
static SDK_HANDLES: LazyLock<Mutex<HashMap<isize, Arc<SdkInstance>>>> = LazyLock::new(|| Mutex::new(HashMap::new()));
static SDK_NEXT_ID: LazyLock<Mutex<isize>> = LazyLock::new(|| Mutex::new(1));

/// SDK 实例
struct SdkInstance {
    config: SdkConfig,
    state: Mutex<SdkState>,
    event_callback: Mutex<Option<ShanLianEventCallback>>,
}

#[derive(Clone, Debug)]
enum SdkState {
    Disconnected,
    Connecting,
    Connected,
    Disconnecting,
    Error(String),
}

impl Default for SdkState {
    fn default() -> Self {
        SdkState::Disconnected
    }
}

/// SDK 配置
#[derive(Debug, Clone)]
pub struct SdkConfig {
    pub controller_url: String,
    pub api_key: Option<String>,
    pub api_secret: Option<String>,
    pub device_id: String,
    pub device_name: Option<String>,
    pub identity_path: Option<String>,
    pub heartbeat_interval: u64,
    pub connection_timeout: u64,
}

/// 隧道类型
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TunnelType {
    Udp,
    Tcp,
    Wss,
    Moon,
}

/// 隧道信息
#[repr(C)]
pub struct ShanLianTunnelInfo {
    pub tunnel_type: c_int,
    pub moon_id: [c_char; 64],
    pub local_ip: [c_char; 16],
    pub remote_ip: [c_char; 16],
    pub local_port: u16,
    pub remote_port: u16,
    pub bytes_sent: u64,
    pub bytes_recv: u64,
    pub rtt_ms: u32,
    pub loss_rate: f32,
    pub active: bool,
}

/// SDK 状态
#[repr(C)]
pub struct ShanLianStatus {
    pub state: c_int,
    pub assigned_ip: [c_char; 16],
    pub network_cidr: [c_char; 8],
    pub gateway_ip: [c_char; 16],
    pub bytes_sent: u64,
    pub bytes_recv: u64,
    pub connected_time: u64,
    pub active_tunnels: c_int,
}

/// 节点质量
#[repr(C)]
pub struct ShanLianNodeQuality {
    pub moon_id: [c_char; 64],
    pub domain: [c_char; 256],
    pub relay_port: u16,
    pub wss_port: u16,
    pub weight: u16,
    pub online: bool,
    pub rtt_ms: u32,
    pub loss_rate: f32,
}

/// 事件类型
#[repr(C)]
#[derive(Debug, Clone, Copy)]
pub enum ShanLianEventType {
    PeerOnline = 0,
    PeerOffline = 1,
    TunnelEstablished = 2,
    TunnelClosed = 3,
    PolicyUpdated = 4,
    Error = 5,
    StateChanged = 6,
}

/// 事件数据联合体
#[repr(C)]
union ShanLianEventData {
    peer_online: ShanLianPeerEvent,
    peer_offline: ShanLianPeerEvent,
    tunnel_established: ShanLianTunnelEvent,
    tunnel_closed: ShanLianTunnelEvent,
    error: ShanLianErrorEvent,
    state_changed: ShanLianStateEvent,
}

#[repr(C)]
#[derive(Debug, Clone, Copy)]
pub struct ShanLianPeerEvent {
    pub peer_id: *const c_char,
}

#[repr(C)]
#[derive(Debug, Clone, Copy)]
pub struct ShanLianTunnelEvent {
    pub tunnel_type: c_int,
    pub moon_id: *const c_char,
}

#[repr(C)]
#[derive(Debug, Clone, Copy)]
pub struct ShanLianErrorEvent {
    pub error_msg: *const c_char,
}

#[repr(C)]
#[derive(Debug, Clone, Copy)]
pub struct ShanLianStateEvent {
    pub old_state: c_int,
    pub new_state: c_int,
}

/// 事件
#[repr(C)]
pub struct ShanLianEvent {
    pub event_type: ShanLianEventType,
    pub user_data: *mut std::ffi::c_void,
    pub data: ShanLianEventData,
}

/// 事件回调类型
pub type ShanLianEventCallback = extern "C" fn(event: *const ShanLianEvent);

/// 错误码
#[repr(C)]
#[derive(Debug, Clone, Copy)]
pub enum ShanLianError {
    Ok = 0,
    InvalidParam = -1,
    NotInitialized = -2,
    AlreadyInit = -3,
    ConnectFailed = -4,
    AuthFailed = -5,
    Timeout = -6,
    Network = -7,
    Internal = -8,
}

impl From<ShanLianError> for c_int {
    fn from(e: ShanLianError) -> Self {
        e as c_int
    }
}

/// C 状态枚举
#[repr(C)]
#[derive(Debug, Clone, Copy)]
pub enum ShanLianState {
    Disconnected = 0,
    Connecting = 1,
    Connected = 2,
    Disconnecting = 3,
    Error = 4,
}

/// C 隧道类型
#[repr(C)]
#[derive(Debug, Clone, Copy)]
pub enum ShanLianTunnelType {
    Udp = 0,
    Tcp = 1,
    Wss = 2,
    Moon = 3,
}

// ============================================================================
// 辅助函数
// ============================================================================

fn ptr_to_string(ptr: *const c_char) -> Option<String> {
    if ptr.is_null() {
        None
    } else {
        unsafe { CStr::from_ptr(ptr) }
            .to_str()
            .ok()
            .map(|s| s.to_string())
    }
}

// ============================================================================
// FFI 导出函数
// ============================================================================

/// 初始化 SDK
#[no_mangle]
pub extern "C" fn shanlian_init(config: *const SdkConfig, handle: *mut ShanLianHandle) -> ShanLianError {
    if config.is_null() || handle.is_null() {
        return ShanLianError::InvalidParam;
    }

    let cfg = unsafe { &*config };

    let sdk_config = SdkConfig {
        controller_url: cfg.controller_url.clone(),
        api_key: cfg.api_key.clone(),
        api_secret: cfg.api_secret.clone(),
        device_id: cfg.device_id.clone(),
        device_name: cfg.device_name.clone(),
        identity_path: cfg.identity_path.clone(),
        heartbeat_interval: cfg.heartbeat_interval,
        connection_timeout: cfg.connection_timeout,
    };

    let instance = Arc::new(SdkInstance {
        config: sdk_config,
        state: Mutex::new(SdkState::Disconnected),
        event_callback: Mutex::new(None),
    });

    let mut handles = SDK_HANDLES.lock().unwrap();
    let mut next_id = SDK_NEXT_ID.lock().unwrap();
    let id = *next_id;
    *next_id += 1;
    handles.insert(id, instance);

    unsafe { *handle = id };

    eprintln!("ShanLian SDK initialized with handle: {}", id);
    ShanLianError::Ok
}

/// 设置事件回调
#[no_mangle]
pub extern "C" fn shanlian_set_event_callback(
    handle: ShanLianHandle,
    callback: ShanLianEventCallback,
) -> ShanLianError {
    let handles = SDK_HANDLES.lock().unwrap();
    let instance = match handles.get(&handle) {
        Some(i) => i,
        None => return ShanLianError::NotInitialized,
    };

    let mut cb = instance.event_callback.lock().unwrap();
    *cb = Some(callback);

    ShanLianError::Ok
}

/// 连接 VPN
#[no_mangle]
pub extern "C" fn shanlian_connect(handle: ShanLianHandle) -> ShanLianError {
    let handles = SDK_HANDLES.lock().unwrap();
    let instance = match handles.get(&handle) {
        Some(i) => i,
        None => return ShanLianError::NotInitialized,
    };

    {
        let mut state = instance.state.lock().unwrap();
        if matches!(*state, SdkState::Connected | SdkState::Connecting) {
            return ShanLianError::AlreadyInit;
        }
        *state = SdkState::Connecting;
    }

    eprintln!("VPN connection initiated for handle: {}", handle);

    let mut state = instance.state.lock().unwrap();
    *state = SdkState::Connected;

    ShanLianError::Ok
}

/// 断开 VPN
#[no_mangle]
pub extern "C" fn shanlian_disconnect(handle: ShanLianHandle) -> ShanLianError {
    let handles = SDK_HANDLES.lock().unwrap();
    let instance = match handles.get(&handle) {
        Some(i) => i,
        None => return ShanLianError::NotInitialized,
    };

    {
        let mut state = instance.state.lock().unwrap();
        if matches!(*state, SdkState::Disconnected | SdkState::Disconnecting) {
            return ShanLianError::Ok;
        }
        *state = SdkState::Disconnecting;
    }

    eprintln!("VPN disconnection initiated for handle: {}", handle);

    let mut state = instance.state.lock().unwrap();
    *state = SdkState::Disconnected;

    ShanLianError::Ok
}

/// 获取状态
#[no_mangle]
pub extern "C" fn shanlian_status(handle: ShanLianHandle, status: *mut ShanLianStatus) -> ShanLianError {
    if status.is_null() {
        return ShanLianError::InvalidParam;
    }

    let handles = SDK_HANDLES.lock().unwrap();
    let instance = match handles.get(&handle) {
        Some(i) => i,
        None => return ShanLianError::NotInitialized,
    };

    let state = instance.state.lock().unwrap();
    let sdk_state = match &*state {
        SdkState::Connected => ShanLianState::Connected,
        SdkState::Connecting => ShanLianState::Connecting,
        SdkState::Disconnecting => ShanLianState::Disconnecting,
        SdkState::Error(_) => ShanLianState::Error,
        SdkState::Disconnected => ShanLianState::Disconnected,
    };

    let status_inner = unsafe { &mut *status };
    status_inner.state = sdk_state as c_int;
    status_inner.assigned_ip = [0; 16];
    status_inner.network_cidr = [0; 8];
    status_inner.gateway_ip = [0; 16];
    status_inner.bytes_sent = 0;
    status_inner.bytes_recv = 0;
    status_inner.connected_time = 0;
    status_inner.active_tunnels = 0;

    ShanLianError::Ok
}

/// 轮询事件
#[no_mangle]
pub extern "C" fn shanlian_poll(handle: ShanLianHandle, timeout_ms: c_int) -> ShanLianError {
    let handles = SDK_HANDLES.lock().unwrap();
    let _instance = match handles.get(&handle) {
        Some(i) => i,
        None => return ShanLianError::NotInitialized,
    };

    std::thread::sleep(std::time::Duration::from_millis(timeout_ms as u64));

    ShanLianError::Ok
}

/// 连接指定节点
#[no_mangle]
pub extern "C" fn shanlian_connect_peer(handle: ShanLianHandle, peer_id: *const c_char) -> ShanLianError {
    if peer_id.is_null() {
        return ShanLianError::InvalidParam;
    }

    let _handles = SDK_HANDLES.lock().unwrap();
    let _instance = match _handles.get(&handle) {
        Some(i) => i,
        None => return ShanLianError::NotInitialized,
    };

    let peer = match ptr_to_string(peer_id) {
        Some(s) => s,
        None => return ShanLianError::InvalidParam,
    };

    eprintln!("Connect to peer: {} for handle: {}", peer, handle);

    ShanLianError::Ok
}

/// 断开指定节点
#[no_mangle]
pub extern "C" fn shanlian_disconnect_peer(handle: ShanLianHandle, peer_id: *const c_char) -> ShanLianError {
    if peer_id.is_null() {
        return ShanLianError::InvalidParam;
    }

    let _handles = SDK_HANDLES.lock().unwrap();
    let _instance = match _handles.get(&handle) {
        Some(i) => i,
        None => return ShanLianError::NotInitialized,
    };

    let peer = match ptr_to_string(peer_id) {
        Some(s) => s,
        None => return ShanLianError::InvalidParam,
    };

    eprintln!("Disconnect from peer: {} for handle: {}", peer, handle);

    ShanLianError::Ok
}

/// 获取隧道列表
#[no_mangle]
pub extern "C" fn shanlian_list_tunnels(
    handle: ShanLianHandle,
    tunnels: *mut *mut ShanLianTunnelInfo,
    count: *mut c_int,
) -> ShanLianError {
    if tunnels.is_null() || count.is_null() {
        return ShanLianError::InvalidParam;
    }

    let _handles = SDK_HANDLES.lock().unwrap();
    let _instance = match _handles.get(&handle) {
        Some(i) => i,
        None => return ShanLianError::NotInitialized,
    };

    let empty: Vec<ShanLianTunnelInfo> = Vec::new();
    let box_ptr = Box::into_raw(empty.into_boxed_slice()) as *mut ShanLianTunnelInfo;

    unsafe {
        *tunnels = box_ptr;
        *count = 0;
    }

    ShanLianError::Ok
}

/// 释放隧道列表内存
#[no_mangle]
pub extern "C" fn shanlian_free_tunnels(
    _handle: ShanLianHandle,
    tunnels: *mut ShanLianTunnelInfo,
    count: c_int,
) {
    if !tunnels.is_null() && count > 0 {
        unsafe {
            let slice = std::slice::from_raw_parts_mut(tunnels, count as usize);
            drop(Box::from_raw(slice as *mut [ShanLianTunnelInfo]));
        }
    }
}

/// 切换隧道类型
#[no_mangle]
pub extern "C" fn shanlian_switch_tunnel(handle: ShanLianHandle, tunnel_type: ShanLianTunnelType) -> ShanLianError {
    let _handles = SDK_HANDLES.lock().unwrap();
    let _instance = match _handles.get(&handle) {
        Some(i) => i,
        None => return ShanLianError::NotInitialized,
    };

    eprintln!("Switch tunnel type to: {:?} for handle: {}", tunnel_type, handle);

    ShanLianError::Ok
}

/// 获取节点列表
#[no_mangle]
pub extern "C" fn shanlian_list_nodes(
    handle: ShanLianHandle,
    nodes: *mut *mut ShanLianNodeQuality,
    count: *mut c_int,
) -> ShanLianError {
    if nodes.is_null() || count.is_null() {
        return ShanLianError::InvalidParam;
    }

    let _handles = SDK_HANDLES.lock().unwrap();
    let _instance = match _handles.get(&handle) {
        Some(i) => i,
        None => return ShanLianError::NotInitialized,
    };

    let empty: Vec<ShanLianNodeQuality> = Vec::new();
    let box_ptr = Box::into_raw(empty.into_boxed_slice()) as *mut ShanLianNodeQuality;

    unsafe {
        *nodes = box_ptr;
        *count = 0;
    }

    ShanLianError::Ok
}

/// 释放节点列表内存
#[no_mangle]
pub extern "C" fn shanlian_free_nodes(
    _handle: ShanLianHandle,
    nodes: *mut ShanLianNodeQuality,
    count: c_int,
) {
    if !nodes.is_null() && count > 0 {
        unsafe {
            let slice = std::slice::from_raw_parts_mut(nodes, count as usize);
            drop(Box::from_raw(slice as *mut [ShanLianNodeQuality]));
        }
    }
}

/// 获取错误信息
#[no_mangle]
pub extern "C" fn shanlian_get_error(handle: ShanLianHandle) -> *const c_char {
    let handles = SDK_HANDLES.lock().unwrap();
    let instance = match handles.get(&handle) {
        Some(i) => i,
        None => return ptr::null(),
    };

    let state = instance.state.lock().unwrap();
    let msg = match &*state {
        SdkState::Error(e) => e.clone(),
        _ => String::new(),
    };

    if msg.is_empty() {
        return ptr::null();
    }

    let c_string = CString::new(msg).unwrap_or_default();
    let ptr = c_string.as_ptr();
    std::mem::forget(c_string);
    ptr
}

/// 销毁 SDK
#[no_mangle]
pub extern "C" fn shanlian_destroy(handle: ShanLianHandle) -> ShanLianError {
    let mut handles = SDK_HANDLES.lock().unwrap();
    match handles.remove(&handle) {
        Some(_) => {
            eprintln!("ShanLian SDK destroyed for handle: {}", handle);
            ShanLianError::Ok
        }
        None => ShanLianError::NotInitialized,
    }
}
