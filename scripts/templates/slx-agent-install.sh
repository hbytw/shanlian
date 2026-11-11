#!/bin/sh
#
# 闪连 SLX 出口一键安装脚本
# 由 LuCI「闪连网关 → 部署出口」页面自动生成
#
# 用法：在境外 VPS 上直接执行
#   sh install.sh
#
# 做的事：
#   1. 检测架构
#   2. 从网关下载对应的 slx 二进制
#   3. 创建非 root 用户
#   4. 写入配置（密钥与网关地址已内置）
#   5. 注册 systemd 服务并设为开机自启
#   6. 启动并做连接体检
#
set -u

KEY="{{KEY}}"
GATEWAY="{{GATEWAY}}"
BINBASE="{{BINBASE}}"
SERVICE="slx"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/slx"
CONFIG="${CONFIG_DIR}/config.yaml"
AGENT_USER="slxagent"

# 公网直连（双通道）相关。
# TLS_LISTEN：本机监听地址（网关来连我）；ADVERTISE：本机公网地址（从外面怎么找到我）。
# 两者不同，别填反。留空 ADVERTISE 表示「还没想好公网 IP」，此时只有 ZeroTier 通道。
TLS_LISTEN="${TLS_LISTEN:-0.0.0.0:8443}"
ADVERTISE="${ADVERTISE:-}"

if [ -t 1 ]; then
	R='\033[31m'; G='\033[32m'; Y='\033[33m'; B='\033[36m'; D='\033[2m'; N='\033[0m'
else
	R=''; G=''; Y=''; B=''; D=''; N=''
fi
info() { printf "${B}==>${N} %s\n" "$*"; }
ok()   { printf "  ${G}✓${N} %s\n" "$*"; }
warn() { printf "  ${Y}!${N} %s\n" "$*"; }
bad()  { printf "  ${R}✗${N} %s\n" "$*"; }
die()  { printf "${R}错误:${N} %s\n" "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "需要 root。请用 sudo sh install.sh"

# ---------------------------------------------------------------- 架构

ARCH=""
case "$(uname -m)" in
	x86_64|amd64)   ARCH="amd64" ;;
	aarch64|arm64)  ARCH="arm64" ;;
	armv7l|armv6l)  ARCH="arm" ;;
	mipsle|mipsel)  ARCH="mipsle" ;;
	riscv64)        ARCH="riscv64" ;;
	*) die "不支持的架构: $(uname -m)" ;;
esac

info "架构: ${ARCH}  系统: $(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME" || echo unknown)"

# ---------------------------------------------------------------- 配置文件检查

[ -n "$KEY" ] || die "脚本里没有密钥。请回到 LuCI 页面重新生成。"
[ -n "$GATEWAY" ] || die "脚本里没有网关地址。请回到 LuCI 页面重新生成。"

# ---------------------------------------------------------------- 下载二进制

info "下载程序"
TMPBIN="/tmp/slx-dl-$$"
UA="slx-installer/1.0"

download() {
	url="$1"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL -A "$UA" --connect-timeout 15 --max-time 300 "$url" -o "$TMPBIN" 2>/dev/null
	elif command -v wget >/dev/null 2>&1; then
		wget -q -U "$UA" --timeout=300 -O "$TMPBIN" "$url" 2>/dev/null
	else
		return 1
	fi
}

TRIED=""
GOT=""
for base in "$BINBASE" "http://${GATEWAY%:*}/slx"; do
	url="${base}/slx-linux-${ARCH}"
	TRIED="${TRIED}  ${url}\n"
	if download "$url" && [ -s "$TMPBIN" ]; then
		# 校验：不能是 HTML 错误页
		if head -c 4 "$TMPBIN" 2>/dev/null | grep -q '<'; then
			continue
		fi
		GOT="$url"
		break
	fi
done

if [ -z "$GOT" ]; then
	bad "下载失败，已尝试："
	printf "$TRIED"
	echo ""
	info "替代方案：手动把 slx-linux-${ARCH} 传到本机，然后执行"
	echo "    scp slx-linux-${ARCH} root@<本机>:/tmp/slx"
	echo "    chmod +x /tmp/slx && /tmp/slx version"
	echo ""
	echo "  再重新运行本脚本（脚本会优先使用 /tmp/slx）。"
	exit 1
fi

ok "$GOT"
chmod 755 "$TMPBIN"

# 兼容：用户在 /tmp/slx 放好了二进制时优先用它
if [ -x /tmp/slx ]; then
	ok "使用已存在的 /tmp/slx"
	TMPBIN=/tmp/slx
fi

if ! "$TMPBIN" version >/dev/null 2>&1; then
	die "下载的文件无法执行（架构不匹配？）。检查本机架构: $(uname -m)"
fi

# ---------------------------------------------------------------- 安装

info "安装程序"
install -m 0755 "$TMPBIN" "${INSTALL_DIR}/slx"
ok "${INSTALL_DIR}/slx  $("$INSTALL_DIR/slx" version 2>&1 | head -1)"

info "创建运行用户（非 root）"
# 不同发行版的 useradd/adduser 参数差异很大：
#   - Debian/Ubuntu 的 adduser 是 perl 脚本，不吃 -r/-S；
#   - CentOS/RHEL 的 /sbin/nologin，Ubuntu 是 /usr/sbin/nologin；
# 所以逐条试，最后必须用 id 验证 —— 否则 systemd 会报
# "Failed to determine user credentials: No such process" 并无限重启。
if ! id "$AGENT_USER" >/dev/null 2>&1; then
	NOLOGIN=/sbin/nologin
	[ -x /usr/sbin/nologin ] && NOLOGIN=/usr/sbin/nologin
	for attempt in \
		"useradd -r -s $NOLOGIN -d /var/lib/slx -M $AGENT_USER" \
		"useradd -r -s $NOLOGIN -d /var/lib/slx $AGENT_USER" \
		"useradd -r -d /var/lib/slx $AGENT_USER" \
		"adduser --system --no-create-home --shell $NOLOGIN $AGENT_USER" \
		"adduser -S -H -s $NOLOGIN $AGENT_USER"
	do
		$attempt >/dev/null 2>&1 && id "$AGENT_USER" >/dev/null 2>&1 && break
	done
fi

RUN_USER="$AGENT_USER"
if id "$AGENT_USER" >/dev/null 2>&1; then
	ok "已创建 ${AGENT_USER}"
else
	# 兜底：建不出来就以 root 运行，绝不因为这一步让服务起不来。
	# 出口本来就不需要 root 权限，用 root 只是少了隔离，功能不受影响。
	RUN_USER=root
	warn "无法创建 ${AGENT_USER}，服务将以 root 运行（功能不受影响，仅少一层隔离）"
fi

info "写入配置"
mkdir -p "$CONFIG_DIR" /var/lib/slx
cat > "$CONFIG" <<EOF
schema_version: 1
network_key: "${KEY}"
gateway_addr: "${GATEWAY}"
transport_mode: proxy
log_level: info
log_format: text
# —— 公网直连（双通道）——
#
# 不写这三项的话，这个 agent 永远只有 ZeroTier 一条通道：
# 网关 auto 模式下也建不出直连，症状极其隐蔽（agent 侧只打一条 Warn）。
#
# relay_tls_listen 是**本机监听**的地址（host:port），网关会来连它；
# relay_advertise 是**本机的公网地址**（要能被网关拨通）。
# 两者不同：一个是"我在哪听"，一个是"从外面怎么找到我"。
relay_tls_listen: "${TLS_LISTEN}"
relay_advertise: "${ADVERTISE}"
# 证书必须放在 agent 用户可写的地方：服务以非 root 跑，
# 写不了 /etc/slx 下的文件（默认路径 /etc/slx/relay-cert.pem 就是 root 属主）。
relay_tls_cert: "/var/lib/slx/relay-cert.pem"
EOF
# 配置里含密钥；服务以非 root 跑，所以属主必须是它
chown "$RUN_USER" "$CONFIG" 2>/dev/null || true
chmod 600 "$CONFIG" 2>/dev/null || true
chown -R "$RUN_USER" /var/lib/slx 2>/dev/null || true
ok "$CONFIG"

# ---------------------------------------------------------------- 服务

info "注册服务"
if [ -d /run/systemd/system ] || command -v systemctl >/dev/null 2>&1; then
	cat > /etc/systemd/system/${SERVICE}.service <<EOF
[Unit]
Description=SLX 出口节点（反向连接网关）
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${RUN_USER}
ExecStart=${INSTALL_DIR}/slx agent --config ${CONFIG}
Restart=on-failure
# 退避 30 秒（与应用内 30 秒重连退避一致）。
# 之前是 5 秒：网络抖动时 agent 会疯狂重启，
# 而每次重启都要重新握手 —— 家宽上行小的时候会把带宽吃光。
RestartSec=30
LimitNOFILE=65536
TimeoutStopSec=15
StandardOutput=journal
StandardError=journal
SyslogIdentifier=slx

[Install]
WantedBy=multi-user.target
EOF
	systemctl daemon-reload
	systemctl enable ${SERVICE} >/dev/null 2>&1
	systemctl restart ${SERVICE}
	ok "systemd 服务已注册并开机自启"
	INIT="systemd"
elif command -v rc-update >/dev/null 2>&1; then
	cat > /etc/init.d/${SERVICE} <<EOF
#!/sbin/openrc-run
name="slx"
command="${INSTALL_DIR}/slx"
command_args="agent --config ${CONFIG}"
command_background=true
pidfile="/run/slx.pid"
command_user="${AGENT_USER}"
output_log="/var/log/slx.log"
error_log="/var/log/slx.log"
# 退避 30 秒，与应用内退避一致；之前是 5 秒（网络抖动时疯狂重启）。
respawn_delay=30
respawn_max=0
depend() { need net; }
EOF
	chmod 755 /etc/init.d/${SERVICE}
	rc-update add ${SERVICE} default >/dev/null 2>&1
	rc-service ${SERVICE} restart >/dev/null 2>&1
	ok "OpenRC 服务已注册并开机自启"
	INIT="openrc"
else
	warn "未识别的 init 系统，请手动启动: ${INSTALL_DIR}/slx agent --config ${CONFIG}"
	INIT="none"
fi

# ---------------------------------------------------------------- 体检

sleep 5

echo ""
info "连接体检"

LOG=""
if [ "$INIT" = "systemd" ]; then
	LOG="$(journalctl -u ${SERVICE} --no-pager -n 80 2>/dev/null || true)"
elif [ "$INIT" = "openrc" ]; then
	LOG="$(tail -80 /var/log/slx.log 2>/dev/null || true)"
fi

FAIL=0

# 1. 服务在跑
if [ "$INIT" = "systemd" ]; then
	if systemctl is-active --quiet ${SERVICE}; then ok "服务运行中"; else bad "服务未运行"; FAIL=1; fi
else
	if pgrep -f "${INSTALL_DIR}/slx" >/dev/null 2>&1; then ok "进程存在"; else bad "进程不存在"; FAIL=1; fi
fi

# 2. 能否到网关
GW_HOST="${GATEWAY%:*}"
GW_PORT="${GATEWAY##*:}"
if command -v nc >/dev/null 2>&1; then
	if nc -z -w 5 "$GW_HOST" "$GW_PORT" 2>/dev/null; then
		ok "网关 ${GATEWAY} 端口可达"
	else
		bad "网关 ${GATEWAY} 端口不可达"
		FAIL=1
	fi
elif command -v timeout >/dev/null 2>&1; then
	if timeout 5 sh -c "cat < /dev/null > /dev/tcp/${GW_HOST}/${GW_PORT}" 2>/dev/null; then
		ok "网关 ${GATEWAY} 端口可达"
	else
		bad "网关 ${GATEWAY} 端口不可达"
		FAIL=1
	fi
else
	warn "没有 nc/timeout，跳过端口连通性检查"
fi

# 3. 认证与连接
if echo "$LOG" | grep -q 'relay.connected'; then
	ok "已连接到网关"
	echo "$LOG" | grep 'relay.connected' | tail -1 | sed "s/^/    ${D}/;s/$/${N}/"
elif echo "$LOG" | grep -q 'connection refused'; then
	FAIL=1
	bad "网关拒绝连接"
	echo "    ${D}原因：网关的 slx 服务没在跑，或 agent_listen 端口不对${N}"
	echo "    ${D}处理：在网关路由器上确认「闪连网关」已启用、监听端口是 ${GW_PORT}${N}"
elif echo "$LOG" | grep -q 'no route to host'; then
	FAIL=1
	bad "网络不可达"
	echo "    ${D}原因：这台机器到 ${GW_HOST} 没有路由${N}"
	echo "    ${D}处理：网关在家宽 NAT 后面时必须走 ZeroTier —— 先确认 ping ${GW_HOST} 能通${N}"
elif echo "$LOG" | grep -qE '密钥不匹配|认证失败'; then
	FAIL=1
	bad "密钥不匹配"
	echo "    ${D}原因：本机配置的 network_key 与网关不一致${N}"
	echo "    ${D}处理：回到 LuCI 页面重新生成安装脚本（脚本里带的是当前密钥）${N}"
elif echo "$LOG" | grep -q '时间戳\|时钟'; then
	FAIL=1
	bad "时钟偏差过大"
	echo "    ${D}处理：timedatectl set-ntp true   或   ntpdate pool.ntp.org${N}"
else
	FAIL=1
	bad "未看到连接结果"
fi

# 4. 出网能力
if command -v curl >/dev/null 2>&1; then
	CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 https://api.ipify.org 2>/dev/null || echo 000)"
	IP="$(curl -s --max-time 10 https://api.ipify.org 2>/dev/null || echo '')"
	if [ "$CODE" = "200" ]; then
		ok "本机出网正常，出口 IP ${IP}"
	else
		warn "本机出网异常（HTTP ${CODE}），请先确认这台机器自己能上网"
	fi
fi

echo ""
if [ "$FAIL" -eq 0 ]; then
	printf "${G}========================================${N}\n"
	printf "${G} 出口部署成功${N}\n"
	printf "${G}========================================${N}\n"
	echo ""
	echo "  网关应该已经显示「已接入 1 个」。"
	echo "  回到 LuCI → 服务 → 闪连网关 → 运行状态 查看。"
	echo ""
	echo "  常用命令："
	echo "    systemctl status slx        # 服务状态"
	echo "    journalctl -u slx -f        # 跟日志"
	echo "    ${INSTALL_DIR}/slx doctor   # 环境体检"
	echo ""
	exit 0
else
	printf "${Y}========================================${N}\n"
	printf "${Y} 部署完成，但连接未成功${N}\n"
	printf "${Y}========================================${N}\n"
	echo ""
	echo "  程序和开机自启都已装好，只是还连不上网关。"
	echo ""
	echo "  最近日志："
	echo "$LOG" | tail -15 | sed 's/^/    /'
	echo ""
	echo "  排查建议："
	echo "    1. 网关是否启用：回到 LuCI 看「运行状态」"
	echo "    2. 能否到网关：ping ${GW_HOST}"
	echo "    3. 端口是否通：nc -zv ${GW_HOST} ${GW_PORT}"
	echo "    4. 密钥是否一致：回 LuCI 页面重新生成脚本"
	echo ""
	echo "  修好后重启服务即可生效："
	echo "    systemctl restart slx"
	echo ""
	exit 2
fi
