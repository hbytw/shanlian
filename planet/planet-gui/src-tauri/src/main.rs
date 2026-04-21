//! 闪连 VPN GUI 主入口

#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    planet_gui::run();
}
