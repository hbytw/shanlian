// 闪连 VPN GUI 前端脚本

// 调试日志
const debugLog = document.getElementById('debug-log');
function debug(msg) {
    console.log('[DEBUG]', msg);
    if (debugLog) {
        const time = new Date().toLocaleTimeString();
        debugLog.innerHTML += `<div>[${time}] ${msg}</div>`;
        debugLog.style.display = 'block';
    }
}

debug('main.js 开始加载');

// 状态
let isConnected = false;
let isConnecting = false;
let invoke, listen;

// DOM 元素
const statusIndicator = document.getElementById('status-indicator');
const statusText = document.getElementById('status-text');
const serverText = document.getElementById('server-text');
const connectBtn = document.getElementById('connect-btn');
const btnText = document.getElementById('btn-text');
const serverInput = document.getElementById('server-input');
const deviceIdInput = document.getElementById('device-id');
const heartbeatInput = document.getElementById('heartbeat-interval');
const autostartToggle = document.getElementById('autostart-toggle');
const logLevelSelect = document.getElementById('log-level');
const saveBtn = document.getElementById('save-btn');
const minimizeBtn = document.getElementById('minimize-btn');

// 显示提示
function showToast(message, type = 'info') {
    debug('[' + type.toUpperCase() + '] ' + message);
    const toast = document.createElement('div');
    toast.className = 'toast toast-' + type;
    toast.textContent = message;
    toast.style.cssText = `
        position: fixed;
        bottom: 20px;
        left: 50%;
        transform: translateX(-50%);
        background: ${type === 'error' ? '#F44336' : '#333'};
        color: white;
        padding: 12px 24px;
        border-radius: 6px;
        z-index: 1000;
        animation: fadeIn 0.3s;
    `;
    document.body.appendChild(toast);
    setTimeout(() => {
        toast.style.animation = 'fadeOut 0.3s';
        setTimeout(() => toast.remove(), 300);
    }, 2000);
}

// 加载配置
async function loadConfig() {
    try {
        const config = await invoke('get_config');
        serverInput.value = config.controller || '';
        deviceIdInput.value = config.device_id || '';
        heartbeatInput.value = config.heartbeat_interval || 30;
        logLevelSelect.value = config.log_level || 'info';
    } catch (e) {
        console.error('加载配置失败:', e);
    }
}

// 保存配置
async function saveConfig() {
    try {
        const config = {
            controller: serverInput.value,
            device_id: deviceIdInput.value || null,
            tun_name: 'planet0',
            heartbeat_interval: parseInt(heartbeatInput.value) || 30,
            log_level: logLevelSelect.value
        };
        await invoke('save_config', { config });
        showToast('设置已保存');
    } catch (e) {
        console.error('保存配置失败:', e);
        showToast('保存失败: ' + e, 'error');
    }
}

// 加载自启动状态
async function loadAutostartStatus() {
    try {
        const enabled = await invoke('get_autostart');
        autostartToggle.checked = enabled;
    } catch (e) {
        console.error('获取自启动状态失败:', e);
    }
}

// 设置自启动
async function setAutostart(enabled) {
    try {
        await invoke('set_autostart', { enabled });
        showToast(enabled ? '已启用自启动' : '已禁用自启动');
    } catch (e) {
        console.error('设置自启动失败:', e);
        showToast('设置自启动失败', 'error');
    }
}

// 加载版本
async function loadVersion() {
    try {
        const version = await invoke('get_version');
        document.getElementById('version').textContent = 'v' + version;
    } catch (e) {
        console.error('获取版本失败:', e);
    }
}

// 刷新连接状态
async function refreshStatus() {
    try {
        const status = await invoke('get_status');
        updateStatusUI(status);
    } catch (e) {
        console.error('获取状态失败:', e);
    }
}

// 更新状态 UI
function updateStatusUI(status) {
    isConnected = status.connected;

    statusIndicator.classList.remove('connected', 'connecting', 'disconnected');

    if (status.connected) {
        statusIndicator.classList.add('connected');
        statusText.textContent = '已连接';
        btnText.textContent = '断开';
        connectBtn.classList.remove('btn-primary');
        connectBtn.classList.add('btn-secondary');
    } else {
        statusIndicator.classList.add('disconnected');
        statusText.textContent = '未连接';
        btnText.textContent = '连接';
        connectBtn.classList.remove('btn-secondary');
        connectBtn.classList.add('btn-primary');
    }

    serverText.textContent = status.server || '--';
}

// 连接/断开
async function toggleConnection() {
    if (isConnected) {
        await disconnect();
    } else {
        await connect();
    }
}

// 连接
async function connect() {
    if (isConnecting) return;

    isConnecting = true;
    statusIndicator.classList.remove('connected', 'disconnected');
    statusIndicator.classList.add('connecting');
    statusText.textContent = '连接中...';
    btnText.textContent = '连接中...';
    connectBtn.disabled = true;

    try {
        const server = serverInput.value || 'https://controller.ytw.fr:8443';
        debug('调用连接，服务器: ' + server);
        const status = await invoke('connect', { server });
        debug('连接响应: ' + JSON.stringify(status));
        updateStatusUI(status);
        showToast('连接成功');
    } catch (e) {
        console.error('连接失败:', e);
        showToast('连接失败: ' + e, 'error');
        statusIndicator.classList.remove('connecting');
        statusIndicator.classList.add('disconnected');
        statusText.textContent = '连接失败';
        btnText.textContent = '连接';
    } finally {
        isConnecting = false;
        connectBtn.disabled = false;
    }
}

// 断开
async function disconnect() {
    try {
        await invoke('disconnect');
        updateStatusUI({ connected: false, server: '', ip: '', uptime_secs: 0, last_error: null });
        showToast('已断开');
    } catch (e) {
        console.error('断开失败:', e);
        showToast('断开失败: ' + e, 'error');
    }
}

// 监听托盘事件
async function listenTrayEvents() {
    await listen('tray-connect', () => {
        if (!isConnected) {
            connect();
        }
    });

    await listen('tray-disconnect', () => {
        if (isConnected) {
            disconnect();
        }
    });
}

// 最小化到托盘
async function minimizeToTray() {
    const { getCurrentWindow } = window.__TAURI__.window;
    const window = getCurrentWindow();
    await window.hide();
}

// 初始化
async function init() {
    debug('初始化中...');

    await loadConfig();
    await loadAutostartStatus();
    await loadVersion();
    await listenTrayEvents();
    await refreshStatus();

    debug('初始化完成');
}

// 事件绑定
document.addEventListener('DOMContentLoaded', () => {
    debug('DOM 加载完成');

    // 轮询等待 Tauri API 初始化
    let retries = 0;
    const maxRetries = 50;
    const checkTauri = setInterval(() => {
        retries++;
        debug('检查 Tauri API... (尝试 ' + retries + ')');
        if (window.__TAURI__) {
            clearInterval(checkTauri);
            invoke = window.__TAURI__.core.invoke;
            listen = window.__TAURI__.event.listen;
            debug('Tauri API 加载成功');
            bindEvents();
            init();
        } else if (retries >= maxRetries) {
            clearInterval(checkTauri);
            debug('ERROR: Tauri API 初始化超时!');
            showToast('Tauri API 初始化失败', 'error');
        }
    }, 100);
});

function bindEvents() {
    debug('绑定事件处理器');

    // 连接按钮
    connectBtn.addEventListener('click', () => {
        debug('connectBtn 点击事件触发');
        toggleConnection();
    });

    // 保存按钮
    saveBtn.addEventListener('click', () => {
        debug('saveBtn 点击事件触发');
        saveConfig();
    });

    // 最小化按钮
    minimizeBtn.addEventListener('click', minimizeToTray);

    // 自启动开关
    autostartToggle.addEventListener('change', (e) => {
        setAutostart(e.target.checked);
    });
}