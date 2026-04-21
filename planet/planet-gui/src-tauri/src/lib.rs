//! 闪连 VPN GUI 命令模块

pub mod commands;

use tauri::{Emitter, Manager};

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    // 获取 exe 所在目录作为日志目录
    let exe_dir = std::env::current_exe()
        .ok()
        .and_then(|p| p.parent().map(|p| p.to_path_buf()))
        .unwrap_or_else(|| std::env::temp_dir());

    let log_dir = exe_dir.join("logs");
    let _ = std::fs::create_dir_all(&log_dir);
    let log_file = log_dir.join("planet-gui.log");

    // 写入启动日志
    if let Ok(mut f) = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(&log_file)
    {
        use std::io::Write;
        let _ = writeln!(f, "\n=== 启动时刻: {:?} ===", std::time::SystemTime::now());
    }

    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::from_default_env()
                .add_directive(tracing::Level::DEBUG.into()),
        )
        .init();

    tracing::info!("闪连 VPN GUI 启动中...");
    tracing::info!("日志文件: {:?}", log_file);

    let builder = tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(
            tauri_plugin_autostart::init(
                tauri_plugin_autostart::MacosLauncher::LaunchAgent,
                Some(vec!["--minimized"]),
            ),
        )
        .setup(|app| {
            tracing::info!("执行 setup...");

            match setup_tray(app) {
                Ok(_) => tracing::info!("系统托盘初始化成功"),
                Err(e) => {
                    tracing::error!("系统托盘初始化失败: {}", e);
                }
            }

            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            commands::config::get_config,
            commands::config::save_config,
            commands::connection::connect,
            commands::connection::disconnect,
            commands::connection::get_status,
            commands::system::get_autostart,
            commands::system::set_autostart,
            commands::system::get_version,
        ]);

    tracing::info!("开始运行 Tauri...");

    match builder.run(tauri::generate_context!()) {
        Ok(_) => tracing::info!("Tauri 应用正常退出"),
        Err(e) => {
            tracing::error!("Tauri 运行错误: {}", e);
            eprintln!("错误: {}", e);
            // 写入错误到日志文件
            if let Ok(mut f) = std::fs::OpenOptions::new()
                .create(true)
                .append(true)
                .open(&log_file)
            {
                use std::io::Write;
                let _ = writeln!(f, "启动错误: {}", e);
            }
        }
    }

    tracing::info!("程序结束");
}

fn setup_tray(app: &tauri::App) -> Result<(), Box<dyn std::error::Error>> {
    use tauri::menu::{MenuBuilder, MenuItemBuilder};
    use tauri::tray::TrayIconBuilder;

    tracing::info!("初始化系统托盘...");

    let icon = app.default_window_icon()
        .cloned()
        .ok_or("没有默认窗口图标")?;

    tracing::info!("获取到图标");

    let show = MenuItemBuilder::with_id("show", "显示主窗口")
        .build(app)?;
    let connect = MenuItemBuilder::with_id("connect", "连接")
        .build(app)?;
    let disconnect = MenuItemBuilder::with_id("disconnect", "断开")
        .build(app)?;
    let separator = tauri::menu::PredefinedMenuItem::separator(app)?;
    let quit = MenuItemBuilder::with_id("quit", "退出")
        .build(app)?;

    tracing::info!("菜单项创建成功");

    let menu = MenuBuilder::new(app)
        .item(&show)
        .item(&separator)
        .item(&connect)
        .item(&disconnect)
        .item(&separator)
        .item(&quit)
        .build()?;

    tracing::info!("构建托盘图标...");

    let _tray = TrayIconBuilder::new()
        .icon(icon)
        .menu(&menu)
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| {
            let id = event.id.as_ref();
            tracing::info!("托盘菜单事件: {}", id);

            match id {
                "show" => {
                    if let Some(window) = app.get_webview_window("main") {
                        let _ = window.show();
                        let _ = window.set_focus();
                    }
                }
                "connect" => {
                    let _ = app.emit("tray-connect", ());
                }
                "disconnect" => {
                    let _ = app.emit("tray-disconnect", ());
                }
                "quit" => {
                    app.exit(0);
                }
                _ => {}
            }
        })
        .on_tray_icon_event(|tray, event| {
            use tauri::tray::MouseButton;
            use tauri::tray::MouseButtonState;

            if let tauri::tray::TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                let app = tray.app_handle();
                if let Some(window) = app.get_webview_window("main") {
                    let _ = window.show();
                    let _ = window.set_focus();
                }
            }
        })
        .build(app)?;

    tracing::info!("系统托盘初始化完成");
    Ok(())
}
