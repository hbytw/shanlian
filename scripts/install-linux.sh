#!/usr/bin/env bash
#
# 闪连 SLX 一键部署脚本（Linux）
#
# 支持三种角色：
#   gateway  家里路由器 / 服务器做网关（需要 root + TUN）
#   agent    境外 VPS 做出口（不需要 root，不需要 TUN，不需要公网 IP）
#   both     同一台机器既做网关又做出口
#
# 自动处理：架构识别、依赖安装、用户创建、配置生成、服务注册、环境体检。
# 重复执行是安全的：已有配置会被保留。
#
set -euo pipefail

VERSION="0.10.0-m7"
PROG_NAME="slx"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/slx"
CONFIG_FILE="${CONFIG_DIR}/config.yaml"
SERVICE_NAME="slx"
STATE_DIR="/var/lib/slx"
AGENT_USER="slxagent"

# 颜色（非终端时自动关闭）
if [ -t 1 ]; then
    C_RED='\033[31m'; C_GRN='\033[32m'; C_YEL='\033[33m'
    C_BLU='\033[36m'; C_DIM='\033[2m'; C_RST='\033[0m'
else
    C_RED=''; C_GRN=''; C_YEL=''; C_BLU=''; C_DIM=''; C_RST=''
fi

info()  { printf "${C_BLU}==>${C_RST} %s\n" "$*"; }
ok()    { printf "  ${C_GRN}✓${C_RST} %s\n" "$*"; }
warn()  { printf "  ${C_YEL}!${C_RST} %s\n" "$*"; }
err()   { printf "${C_RED}错误:${C_RST} %s\n" "$*" >&2; }
die()   { err "$*"; exit 1; }
dim()   { printf "  ${C_DIM}%s${C_RST}\n" "$*"; }

# ---------------------------------------------------------------- 基础检测

need_root() {
    [ "$(id -u)" -eq 0 ] || die "需要 root 权限。请用 sudo 运行。"
}

detect_arch() {
    local m
    m="$(uname -m)"
    case "$m" in
        x86_64|amd64)   echo "amd64" ;;
        aarch64|arm64)  echo "arm64" ;;
        armv7l|armv6l|arm) echo "arm" ;;
        mipsle|mipsel)  echo "mipsle" ;;
        riscv64)        echo "riscv64" ;;
        *) die "不支持的架构: $m（可用：amd64/arm64/arm/mipsle/riscv64）" ;;
    esac
}

detect_os() {
    if [ -r /etc/os-release ]; then
        # shellcheck disable=SC1091
        . /etc/os-release
        echo "${ID:-unknown}"
    else
        echo "unknown"
    fi
}

detect_init() {
    if [ -d /run/systemd/system ] || command -v systemctl >/dev/null 2>&1; then
        echo "systemd"
    elif command -v rc-service >/dev/null 2>&1; then
        echo "openrc"
    else
        echo "sysvinit"
    fi
}

# ---------------------------------------------------------------- 依赖安装

install_deps() {
    local role="$1"
    local os="$2"

    local pkgs_needed=()
    command -v ip >/dev/null 2>&1 || pkgs_needed+=(iproute2)
    command -v iptables >/dev/null 2>&1 || pkgs_needed+=(iptables)
    command -v ipset >/dev/null 2>&1 || pkgs_needed+=(ipset)
    [ -e /dev/net/tun ] || pkgs_needed+=(kmod-tun)

    # 出口角色不需要 TUN / iptables / ipset，去掉
    if [ "$role" = "agent" ]; then
        pkgs_needed=()
        command -v curl >/dev/null 2>&1 || pkgs_needed+=(curl)
    fi

    if [ "${#pkgs_needed[@]}" -eq 0 ]; then
        ok "依赖已齐全"
        return 0
    fi

    info "安装依赖: ${pkgs_needed[*]}"
    case "$os" in
        ubuntu|debian|raspbian)
            apt-get update -qq >/dev/null 2>&1 || true
            DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${pkgs_needed[@]}" >/dev/null 2>&1 || {
                warn "部分依赖安装失败，可能影响功能"; return 0; }
            ;;
        centos|rhel|rocky|almalinux|fedora|anolis)
            local pm=yum; command -v dnf >/dev/null 2>&1 && pm=dnf
            $pm install -y -q "${pkgs_needed[@]}" >/dev/null 2>&1 || {
                warn "部分依赖安装失败，可能影响功能"; return 0; }
            ;;
        alpine)
            apk add --no-cache "${pkgs_needed[@]}" >/dev/null 2>&1 || {
                warn "部分依赖安装失败"; return 0; }
            ;;
        *)
            warn "未识别的发行版 $os，请手动安装: ${pkgs_needed[*]}"
            ;;
    esac
    ok "依赖安装完成"
}

# ---------------------------------------------------------------- 二进制

resolve_binary() {
    local arch="$1"
    local self_dir
    self_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd)"

    # 1. 同目录下的对应架构文件
    local cand="${self_dir}/slx-linux-${arch}"
    if [ -f "$cand" ]; then
        echo "$cand"; return 0
    fi
    # 2. dist 子目录
    cand="${self_dir}/dist/slx-linux-${arch}"
    if [ -f "$cand" ]; then
        echo "$cand"; return 0
    fi
    # 3. 已安装的版本
    if [ -x "${INSTALL_DIR}/${PROG_NAME}" ]; then
        echo "${INSTALL_DIR}/${PROG_NAME}"; return 0
    fi
    # 4. 从 SLX_BIN_URL 下载
    if [ -n "${SLX_BIN_URL:-}" ]; then
        local tmp="/tmp/slx-dl-${arch}"
        info "从 ${SLX_BIN_URL} 下载" >&2
        curl -fsSL "${SLX_BIN_URL}/slx-linux-${arch}" -o "$tmp" 2>/dev/null || {
            die "下载失败: ${SLX_BIN_URL}/slx-linux-${arch}"; }
        echo "$tmp"; return 0
    fi

    die "找不到 slx 二进制（架构 ${arch}）。
请把 slx-linux-${arch} 放在脚本同目录，或设置环境变量：
  SLX_BIN_URL=https://你的分发地址 ./install-linux.sh ..."
}

install_binary() {
    local src="$1"
    info "安装程序"
    install -m 0755 "$src" "${INSTALL_DIR}/${PROG_NAME}"
    ok "${INSTALL_DIR}/${PROG_NAME}"
    dim "$(${INSTALL_DIR}/${PROG_NAME} version 2>&1 | head -1)"
}

# ---------------------------------------------------------------- 用户

ensure_agent_user() {
    if id "$AGENT_USER" >/dev/null 2>&1; then
        ok "出口用户 ${AGENT_USER} 已存在"
        return 0
    fi
    info "创建出口专用用户（非 root）"
    if command -v useradd >/dev/null 2>&1; then
        useradd -r -s /usr/sbin/nologin -d "${STATE_DIR}" "$AGENT_USER" 2>/dev/null \
            || useradd -r -s /bin/false -d "${STATE_DIR}" "$AGENT_USER" 2>/dev/null \
            || useradd -m -s /bin/bash "$AGENT_USER"
    else
        adduser -S -H -s /sbin/nologin "$AGENT_USER" 2>/dev/null || true
    fi
    ok "已创建 ${AGENT_USER}（出口不需要 root）"
}

# ---------------------------------------------------------------- 配置

# 生成 32 字节随机密钥（base64，44 字符）。
# 不调用 `slx config --key`：那个命令会顺带创建配置文件，
# 导致后面的 write_config 认为「已有配置」而跳过，agent_listen 就丢了。
gen_key() {
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -base64 32 | tr -d '\n'
    else
        head -c 32 /dev/urandom | base64 | tr -d '\n'
    fi
}

write_config() {
    local role="$1" key="$2" gateway_addr="$3" agent_listen="$4" dispatch="$5"
    mkdir -p "$CONFIG_DIR"

    if [ -f "$CONFIG_FILE" ]; then
        ok "保留已有配置 ${CONFIG_FILE}"
        # 只在缺 key 时补上
        if ! grep -q '^network_key:' "$CONFIG_FILE" 2>/dev/null; then
            warn "已有配置里没有 network_key，可能需要手动补"
        fi
        return 0
    fi

    {
        echo "schema_version: 1"
        echo "network_key: \"${key}\""
        echo "transport_mode: proxy"
        echo "log_level: info"
        echo "log_format: text"
        case "$role" in
            gateway|both)
                echo "agent_listen: \"${agent_listen}\""
                echo "dispatch: ${dispatch}"
                ;;
        esac
        case "$role" in
            agent|both)
                echo "gateway_addr: \"${gateway_addr}\""
                ;;
        esac
    } > "$CONFIG_FILE"
    chmod 600 "$CONFIG_FILE"
    ok "${CONFIG_FILE}"
}

# ---------------------------------------------------------------- 服务

install_service() {
    local role="$1" init="$2"

    # 出口角色以非 root 运行；网关必须 root（要建 TUN、写 iptables）
    local run_user="root"
    [ "$role" = "agent" ] && run_user="$AGENT_USER"

    # 出口用 agent 子命令，网关用 run
    local subcmd="run"
    [ "$role" = "agent" ] && subcmd="agent"

    mkdir -p "$STATE_DIR"

    # 关键：服务以非 root 用户运行时，配置文件和状态目录必须归它所有。
    # 否则 systemd 启动后会报 "open /etc/slx/config.yaml: permission denied"，
    # 而且日志里只有 permission denied，看不出是属主问题。
    chown -R "$run_user" "$STATE_DIR" 2>/dev/null || true
    if [ "$run_user" != "root" ]; then
        chown "$run_user" "$CONFIG_FILE" 2>/dev/null || true
        # 配置里含 network_key，保持 600，但属主换成服务用户
        chmod 600 "$CONFIG_FILE" 2>/dev/null || true
        chown "$run_user" "$CONFIG_DIR" 2>/dev/null || true
    fi

    case "$init" in
        systemd) install_systemd_unit "$subcmd" "$run_user" ;;
        openrc)  install_openrc_script "$subcmd" "$run_user" ;;
        *)       install_sysvinit_script "$subcmd" "$run_user" ;;
    esac
}

install_systemd_unit() {
    local subcmd="$1" run_user="$2"
    local unit="/etc/systemd/system/${SERVICE_NAME}.service"

    # 出口需要网络就绪即可；网关还需要 TUN 模块
    local after="network-online.target"
    [ "$subcmd" = "run" ] && after="network-online.target systemd-modules-load.service"

    cat > "$unit" <<UNIT
[Unit]
Description=SLX 智能出口网关（${subcmd}）
Documentation=https://github.com/ytw/slx
After=${after}
Wants=network-online.target

[Service]
Type=simple
User=${run_user}
ExecStart=${INSTALL_DIR}/${PROG_NAME} ${subcmd} --config ${CONFIG_FILE}
Restart=always
RestartSec=5
LimitNOFILE=65536
# 优雅停止：SLX 会自己撤掉 iptables / ipset / TUN
KillSignal=SIGTERM
TimeoutStopSec=15
# 日志进 journald
StandardOutput=journal
StandardError=journal
SyslogIdentifier=slx

[Install]
WantedBy=multi-user.target
UNIT

    systemctl daemon-reload
    systemctl enable "${SERVICE_NAME}" >/dev/null 2>&1
    ok "systemd 服务已注册 ${unit}"
}

install_openrc_script() {
    local subcmd="$1" run_user="$2"
    local f="/etc/init.d/${SERVICE_NAME}"
    cat > "$f" <<RC
#!/sbin/openrc-run
name="slx"
description="SLX 智能出口网关（${subcmd}）"
command="${INSTALL_DIR}/${PROG_NAME}"
command_args="${subcmd} --config ${CONFIG_FILE}"
command_background=true
pidfile="/run/\${name}.pid"
command_user="${run_user}"
output_log="/var/log/slx.log"
error_log="/var/log/slx.log"
respawn_delay=5
respawn_max=0

depend() {
    need net
}
RC
    chmod 0755 "$f"
    rc-update add "$SERVICE_NAME" default >/dev/null 2>&1 || true
    ok "OpenRC 服务已注册 ${f}"
}

install_sysvinit_script() {
    local subcmd="$1" run_user="$2"
    local f="/etc/init.d/${SERVICE_NAME}"
    cat > "$f" <<SYSV
#!/bin/sh
### BEGIN INIT INFO
# Provides:          slx
# Required-Start:    \$network \$remote_fs
# Required-Stop:     \$network \$remote_fs
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: SLX 智能出口网关
### END INIT INFO

DAEMON="${INSTALL_DIR}/${PROG_NAME}"
ARGS="${subcmd} --config ${CONFIG_FILE}"
PIDFILE="/var/run/slx.pid"
RUN_USER="${run_user}"

case "\$1" in
  start)
    echo "启动 slx"
    if command -v start-stop-daemon >/dev/null 2>&1; then
      start-stop-daemon --start --background --make-pidfile --pidfile "\$PIDFILE" \\
        --chuid "\$RUN_USER" --exec "\$DAEMON" -- \$ARGS
    else
      su -s /bin/sh "\$RUN_USER" -c "\$DAEMON \$ARGS &"
    fi
    ;;
  stop)
    echo "停止 slx"
    if command -v start-stop-daemon >/dev/null 2>&1; then
      start-stop-daemon --stop --pidfile "\$PIDFILE" --retry 15
    else
      pkill -f "\$DAEMON" 2>/dev/null || true
    fi
    rm -f "\$PIDFILE"
    ;;
  restart) \$0 stop; sleep 1; \$0 start ;;
  status) pgrep -f "\$DAEMON" >/dev/null && echo "运行中" || echo "已停止" ;;
  *) echo "用法: \$0 {start|stop|restart|status}"; exit 1 ;;
esac
SYSV
    chmod 0755 "$f"
    ok "SysV 服务已注册 ${f}"
}

# ---------------------------------------------------------------- 防火墙提示

hint_firewall() {
    local port="$1"
    info "放行端口 ${port}/tcp（出口节点需要连进来）"

    if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
        firewall-cmd --permanent --add-port="${port}/tcp" >/dev/null 2>&1 || true
        firewall-cmd --reload >/dev/null 2>&1 || true
        ok "firewalld 已放行"
    elif command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q active; then
        ufw allow "${port}/tcp" >/dev/null 2>&1 || true
        ok "ufw 已放行"
    elif command -v iptables >/dev/null 2>&1; then
        # 检查是否已存在，避免重复插入
        if ! iptables -C INPUT -p tcp --dport "$port" -j ACCEPT 2>/dev/null; then
            iptables -I INPUT 1 -p tcp --dport "$port" -j ACCEPT 2>/dev/null || true
            ok "iptables 已放行（未持久化，重启后需重新执行）"
            warn "建议手动保存:  iptables-save > /etc/sysconfig/iptables  或  netfilter-persistent save"
        else
            ok "iptables 已有放行规则"
        fi
    else
        warn "未检测到防火墙工具，如果连接不上请手动放行 ${port}/tcp"
    fi
}

# ---------------------------------------------------------------- 交互

ask() {
    local prompt="$1" default="$2" answer
    if [ -t 0 ]; then
        printf "  %s [%s]: " "$prompt" "$default" >&2
        read -r answer
        echo "${answer:-$default}"
    else
        echo "$default"
    fi
}

ask_secret() {
    local prompt="$1" answer
    if [ -t 0 ]; then
        printf "  %s: " "$prompt" >&2
        read -rs answer
        echo >&2
        echo "$answer"
    else
        echo ""
    fi
}

# ---------------------------------------------------------------- 子命令

cmd_gateway() {
    local key="$1" port="$2"
    need_root
    local arch os init
    arch="$(detect_arch)"; os="$(detect_os)"; init="$(detect_init)"

    info "环境：${os} / ${arch} / ${init}"
    install_deps gateway "$os"

    local bin; bin="$(resolve_binary "$arch")"
    install_binary "$bin"

    if [ -z "$key" ]; then
        key="$(gen_key)"
        info "已生成新的网络密钥"
        echo
        printf "  ${C_YEL}%s${C_RST}\n" "$key"
        echo
        dim "把这段密钥抄到所有出口节点。查看方式:  slx config --key"
        echo
    fi

    write_config gateway "$key" "" "0.0.0.0:${port}" "true"
    install_service gateway "$init"
    hint_firewall "$port"

    info "启动服务"
    systemctl restart "$SERVICE_NAME" 2>/dev/null || \
        rc-service "$SERVICE_NAME" restart 2>/dev/null || \
        "/etc/init.d/${SERVICE_NAME}" restart 2>/dev/null || true
    sleep 3

    verify_gateway "$port"
}

cmd_agent() {
    local key="$1" gateway_addr="$2"
    need_root
    local arch os init
    arch="$(detect_arch)"; os="$(detect_os)"; init="$(detect_init)"

    info "环境：${os} / ${arch} / ${init}"
    install_deps agent "$os"

    local bin; bin="$(resolve_binary "$arch")"
    install_binary "$bin"
    ensure_agent_user

    if [ -z "$key" ]; then
        err "出口需要和网关相同的网络密钥"
        dim "在网关机上执行  slx config --key  取到密钥后："
        dim "  $0 agent --key <密钥> --gateway <网关地址:端口>"
        exit 1
    fi
    if [ -z "$gateway_addr" ]; then
        err "需要指定网关地址"
        dim "  $0 agent --key <密钥> --gateway 1.2.3.4:11601"
        exit 1
    fi

    write_config agent "$key" "$gateway_addr" "" ""
    install_service agent "$init"

    info "启动服务"
    systemctl restart "$SERVICE_NAME" 2>/dev/null || \
        rc-service "$SERVICE_NAME" restart 2>/dev/null || \
        "/etc/init.d/${SERVICE_NAME}" restart 2>/dev/null || true
    sleep 4

    verify_agent
}

cmd_uninstall() {
    need_root
    info "停止并移除服务"
    systemctl stop "$SERVICE_NAME" 2>/dev/null || \
        rc-service "$SERVICE_NAME" stop 2>/dev/null || \
        "/etc/init.d/${SERVICE_NAME}" stop 2>/dev/null || true
    systemctl disable "$SERVICE_NAME" 2>/dev/null || \
        rc-update del "$SERVICE_NAME" default 2>/dev/null || true
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service" "/etc/init.d/${SERVICE_NAME}"
    systemctl daemon-reload 2>/dev/null || true

    rm -f "${INSTALL_DIR}/${PROG_NAME}"
    rm -rf "$CONFIG_DIR" "$STATE_DIR"
    warn "配置与密钥已删除。如需保留，卸载前先备份 ${CONFIG_DIR}"
    ok "卸载完成"
}

# ---------------------------------------------------------------- 验证

verify_gateway() {
    local listen_port="${1:-11601}"
    info "环境体检"
    if "${INSTALL_DIR}/${PROG_NAME}" doctor 2>&1 | grep -qE '失败 [1-9]'; then
        warn "有检查项未通过，请查看上方输出"
    else
        ok "体检通过"
    fi

    info "运行状态"
    local running=""
    systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null && running=1
    rc-service "$SERVICE_NAME" status >/dev/null 2>&1 && running=1
    pgrep -f "${INSTALL_DIR}/${PROG_NAME}" >/dev/null 2>&1 && running=1

    if [ -n "$running" ]; then
        ok "服务运行中"
        sleep 4
        local joined
        joined="$(journalctl -u "$SERVICE_NAME" --no-pager -n 100 2>/dev/null | grep -c 'agent_joined' || true)"
        [ -z "$joined" ] && joined=0
        if [ "$joined" -gt 0 ]; then
            ok "已接入 ${joined} 个出口"
        else
            warn "还没有出口接入"
            dim "在出口节点执行："
            dim "  $0 agent --key <上面的密钥> --gateway <本机公网IP:11601>"
        fi
        echo
        info "当前状态"
        "${INSTALL_DIR}/${PROG_NAME}" status 2>&1 | sed -n '1,16p' | sed 's/^/  /' || true
    else
        err "服务未能启动"
        dim "查看日志:"
        dim "  journalctl -u ${SERVICE_NAME} -n 40 --no-pager"
        dim "  或:  /etc/init.d/${SERVICE_NAME} status"
        exit 1
    fi

    echo
    printf "${C_GRN}========================================${C_RST}\n"
    printf "${C_GRN} 网关部署完成${C_RST}\n"
    printf "${C_GRN}========================================${C_RST}\n"
    echo
    echo "  配置文件:  ${CONFIG_FILE}"
    echo "  密钥:      ${INSTALL_DIR}/${PROG_NAME} config --key"
    echo "  状态:      ${INSTALL_DIR}/${PROG_NAME} status"
    echo "  选路:      ${INSTALL_DIR}/${PROG_NAME} routes"
    echo "  日志:      journalctl -u ${SERVICE_NAME} -f"
    echo ""
    echo "  下一步：在出口节点执行"
    echo "    $0 agent --key <密钥> --gateway $(hostname -I 2>/dev/null | awk '{print $1}' || echo '<本机IP>'):${listen_port}"
    echo
    return 0
}

verify_agent() {
    info "环境体检"
    "${INSTALL_DIR}/${PROG_NAME}" doctor 2>&1 | sed -n '1,20p' | sed 's/^/  /' || true

    info "连接状态"
    sleep 3
    local log
    log="$(journalctl -u "$SERVICE_NAME" --no-pager -n 60 2>/dev/null || \
           tail -60 /var/log/slx.log 2>/dev/null || true)"

    if echo "$log" | grep -q 'relay.connected'; then
        ok "已连接到网关"
        echo "$log" | grep 'relay.connected' | tail -1 | sed 's/^/  /'
    elif echo "$log" | grep -q 'connection refused'; then
        err "网关拒绝连接"
        dim "确认网关已启动、端口开放、地址正确"
    elif echo "$log" | grep -q 'no route to host'; then
        err "网络不可达"
        dim "确认能 ping 通网关地址；家里网关可能需要 ZeroTier 通路"
    elif echo "$log" | grep -qE '密钥不匹配|network_key'; then
        err "密钥不一致"
        dim "核对两端的 network_key：${INSTALL_DIR}/${PROG_NAME} config --key"
    elif echo "$log" | grep -q '时间戳'; then
        err "时钟偏差过大"
        dim "同步时间:  timedatectl set-ntp true  或  ntpdate pool.ntp.org"
    else
        warn "暂未看到连接结果，日志如下："
        echo "$log" | tail -10 | sed 's/^/  /'
    fi

    echo
    printf "${C_GRN}========================================${C_RST}\n"
    printf "${C_GRN} 出口部署完成${C_RST}\n"
    printf "${C_GRN}========================================${C_RST}\n"
    echo
    echo "  配置文件:  ${CONFIG_FILE}"
    echo "  运行用户:  ${AGENT_USER}（非 root）"
    echo "  日志:      journalctl -u ${SERVICE_NAME} -f"
    echo ""
    dim "出口不需要 root / TUN / 公网 IP —— 只要这台机器能上网就能用。"
    echo
    return 0
}

cmd_doctor() {
    [ -x "${INSTALL_DIR}/${PROG_NAME}" ] || die "未安装。先运行 $0 gateway 或 $0 agent"
    "${INSTALL_DIR}/${PROG_NAME}" doctor
}

# ---------------------------------------------------------------- 帮助

usage() {
    cat <<EOF
闪连 SLX 一键部署（Linux）

用法:
  $0 gateway [选项]     部署为网关（需要 root + TUN）
  $0 agent   [选项]     部署为出口（不需要 root）
  $0 uninstall          卸载
  $0 doctor             环境体检

网关选项:
  --key <密钥>          指定网络密钥；不指定则自动生成
  --port <端口>         agent 监听端口，默认 11601

出口选项:
  --key <密钥>          必需，与网关一致
  --gateway <地址>      必需，网关地址，如 1.2.3.4:11601
                        家里网关在 NAT 后面时填 ZeroTier 地址，如 192.168.99.1:11601

例子:
  # 家里路由器 / 服务器
  sudo ./install-linux.sh gateway

  # 境外 VPS
  sudo ./install-linux.sh agent --key "xxxx" --gateway 1.2.3.4:11601

环境变量:
  SLX_BIN_URL           二进制下载地址前缀（默认从脚本同目录找）

EOF
}

# ---------------------------------------------------------------- 主流程

main() {
    local cmd="${1:-}"
    [ $# -gt 0 ] && shift

    local key="" port="11601" gateway_addr=""

    while [ $# -gt 0 ]; do
        case "$1" in
            --key)      key="${2:-}"; shift 2 ;;
            --key=*)    key="${1#*=}"; shift ;;
            --port)     port="${2:-11601}"; shift 2 ;;
            --port=*)   port="${1#*=}"; shift ;;
            --gateway)  gateway_addr="${2:-}"; shift 2 ;;
            --gateway=*) gateway_addr="${1#*=}"; shift ;;
            -h|--help)  usage; exit 0 ;;
            *) die "未知参数: $1（用 --help 查看用法）" ;;
        esac
    done

    case "$cmd" in
        gateway)   cmd_gateway "$key" "$port" ;;
        agent)     cmd_agent "$key" "$gateway_addr" ;;
        uninstall) cmd_uninstall ;;
        doctor)    cmd_doctor ;;
        ""|-h|--help|help) usage ;;
        *) usage; die "未知命令: $cmd" ;;
    esac
}

main "$@"
