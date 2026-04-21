//! 系统相关命令

use tauri::AppHandle;
use tauri_plugin_autostart::ManagerExt;

/// 获取自启动状态
#[tauri::command]
pub async fn get_autostart(app: AppHandle) -> Result<bool, String> {
    let autostart = app.autolaunch();
    autostart.is_enabled().map_err(|e| e.to_string())
}

/// 设置自启动状态
#[tauri::command]
pub async fn set_autostart(app: AppHandle, enabled: bool) -> Result<(), String> {
    let autostart = app.autolaunch();

    if enabled {
        autostart.enable().map_err(|e| e.to_string())?;
        tracing::info!("已启用自启动");
    } else {
        autostart.disable().map_err(|e| e.to_string())?;
        tracing::info!("已禁用自启动");
    }

    Ok(())
}

/// 获取版本信息
#[tauri::command]
pub async fn get_version() -> Result<String, String> {
    Ok(env!("CARGO_PKG_VERSION").to_string())
}
