#!/bin/bash
# Planet VPN 服务安装脚本

set -e

INSTALL_DIR="/usr/local/bin"
SERVICE_DIR="/etc/systemd/system"
CONFIG_DIR="$HOME/.config/planet"

echo "=========================================="
echo "  闪连 VPN 服务安装脚本"
echo "=========================================="

# 检查是否为 root 用户
is_root() {
    [ "$(id -u)" -eq 0 ]
}

# 安装 planet 可执行文件
install_binary() {
    echo ""
    echo "[1/4] 安装 planet 可执行文件..."

    if [ -f "./target/release/planet" ]; then
        sudo cp ./target/release/planet "$INSTALL_DIR/planet"
        sudo chmod +x "$INSTALL_DIR/planet"
        echo "  已安装到 $INSTALL_DIR/planet"
    else
        echo "  错误: 未找到编译好的 planet 可执行文件"
        echo "  请先运行: cargo build --release"
        exit 1
    fi
}

# 创建配置目录
create_config_dir() {
    echo ""
    echo "[2/4] 创建配置目录..."

    mkdir -p "$CONFIG_DIR"
    echo "  配置目录: $CONFIG_DIR"
}

# 安装 systemd 服务
install_systemd_service() {
    echo ""
    echo "[3/4] 安装 systemd 服务..."

    if is_root; then
        sudo cp planet-service/planet.service "$SERVICE_DIR/planet.service"
        sudo systemctl daemon-reload
        echo "  已安装到 $SERVICE_DIR/planet.service"
    else
        echo "  警告: 需要 root 权限安装 systemd 服务"
        echo "  请手动执行以下命令:"
        echo "    sudo cp planet-service/planet.service $SERVICE_DIR/planet.service"
        echo "    sudo systemctl daemon-reload"
    fi
}

# 完成提示
show_summary() {
    echo ""
    echo "[4/4] 安装完成!"
    echo ""
    echo "=========================================="
    echo "  使用方法:"
    echo "=========================================="
    echo ""
    echo "  启动服务:"
    if is_root; then
        echo "    sudo systemctl start planet"
    else
        echo "    systemctl --user start planet"
    fi
    echo ""
    echo "  开机自启:"
    if is_root; then
        echo "    sudo systemctl enable planet"
    else
        echo "    systemctl --user enable planet"
    fi
    echo ""
    echo "  查看状态:"
    if is_root; then
        echo "    sudo systemctl status planet"
    else
        echo "    systemctl --user status planet"
    fi
    echo ""
    echo "  查看日志:"
    if is_root; then
        echo "    sudo journalctl -u planet -f"
    else
        echo "    journalctl --user -u planet -f"
    fi
    echo ""
    echo "  卸载:"
    echo "    sudo systemctl stop planet"
    echo "    sudo systemctl disable planet"
    echo "    sudo rm $SERVICE_DIR/planet.service"
    echo "    sudo rm $INSTALL_DIR/planet"
    echo ""
}

# 运行安装
install_binary
create_config_dir
install_systemd_service
show_summary
