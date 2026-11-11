"""构建 slx 的 OpenWrt .ipk 软件包（含 LuCI 页面）。

包内容：
  /usr/sbin/slx                         二进制（aarch64）
  /etc/config/slx                       UCI 配置
  /etc/init.d/slx                       procd 服务（启动时把 UCI 转成 YAML）
  /usr/lib/lua/luci/controller/slx.lua  菜单注册
  /usr/lib/lua/luci/model/cbi/slx.lua   设置页
  /usr/lib/lua/luci/view/slx/status.htm 状态页
"""
import os, sys, io, io as _io, gzip, tarfile, struct, time, shutil, hashlib

try:
    sys.stdout = _io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
except Exception:
    pass

ROOT = r"E:\code\shanlian"
PKG = "slx"
VERSION = "0.10.0-m7"
ARCH = "aarch64_generic"
BIN = os.path.join(ROOT, "dist", "slx-linux-arm64")
OUTDIR = os.path.join(ROOT, "dist")
STAGE = os.path.join(ROOT, ".pkgstage")

if os.path.exists(STAGE):
    shutil.rmtree(STAGE)
DATA = os.path.join(STAGE, "data")
CTRL = os.path.join(STAGE, "ctrl")
os.makedirs(DATA)
os.makedirs(CTRL)


def write(rel, content, mode=0o644):
    p = os.path.join(DATA, rel.lstrip("/"))
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, "w", encoding="utf-8", newline="\n") as f:
        f.write(content)
    os.chmod(p, mode)


# ---------------- /etc/config/slx（UCI） ----------------
write("/etc/config/slx", """config slx 'main'
\toption enabled '0'
\toption transport_mode 'proxy'
\toption network_key ''
\toption agent_listen '0.0.0.0:11601'
\toption gateway_addr ''
\toption dispatch '1'
\toption dns_listen ''
\toption log_level 'info'
\toption egress ''
\toption relay_transport 'zt'
\toption relay_tls_port '8443'
\toption relay_tls_listen ''
\toption relay_tls_conns '4'
\toption relay_tls_pin ''
\toption relay_advertise ''
\toption relay_clock_skew '60s'
""")

# ---------------- /etc/init.d/slx（procd） ----------------
write("/etc/init.d/slx", r"""#!/bin/sh /etc/rc.common

START=99
STOP=10
USE_PROCD=1

PROG=/usr/sbin/slx
CONF=/etc/config/slx
YAML=/var/etc/slx.yaml

slx_get() {
	uci -q get slx.main."$1" 2>/dev/null
}

# 数值型配置项：只接受纯数字。
#
# 为什么必须校验：yaml.v3 遇到 `relay_channel_failures: 8s` 这类值会报
# "cannot unmarshal !!str into int"，config.go 直接返回错误 → run 退出
# → procd 一小时只重启 5 次就放弃 → **网关彻底起不来且不会自动恢复**。
#
# 行为：非法值**明确报错并跳过该项**（不生成非法 YAML，也不让整个网关起不来）。
slx_num() {
	_v="$(slx_get "$1")"
	if [ -z "$_v" ]; then
		[ -n "$2" ] && echo "$2"
		return 0
	fi
	case "$_v" in
		*[!0-9]*)
			echo "slx: 配置项 $1 必须是纯数字（当前 '$_v'），已忽略该项" >&2
			logger -t slx "配置项 $1 的值 '$_v' 不是合法数字，已忽略"
			return 0
			;;
	esac
	echo "$_v"
}

# 列表型配置项：UCI 里是空格分隔，YAML 里要写成列表。
# 输出形如 "a","b"（不含外层方括号）。
slx_list() {
	_v="$(slx_get "$1")"
	[ -z "$_v" ] && return 0
	printf '%s\n' "$_v" | tr ' ' '\n' | grep -v '^$' | sed 's/.*/"&"/' | paste -sd, -
}

# 布尔型配置项：1/true → true；0/false → false；其它（含未设置）→ 不输出。
slx_bool() {
	case "$(slx_get "$1")" in
		1|true|yes|on) echo "true" ;;
		0|false|no|off) echo "false" ;;
	esac
}

# 把 UCI 配置翻译成 slx 读的 YAML。
# 为什么不直接让 slx 读 UCI：slx 要能在非 OpenWrt 平台上跑，
# 不能为了这一个平台引入 uci 依赖。翻译放在 init 脚本里，代价最小。
gen_yaml() {
	mkdir -p /var/etc
	{
		echo "schema_version: 1"
		echo "transport_mode: $(slx_get transport_mode)"
		echo "log_level: $(slx_get log_level)"

		nk="$(slx_get network_key)"
		[ -n "$nk" ] && echo "network_key: \"$nk\""

		ga="$(slx_get gateway_addr)"
		[ -n "$ga" ] && echo "gateway_addr: \"$ga\""

		al="$(slx_get agent_listen)"
		[ -n "$al" ] && echo "agent_listen: \"$al\""

		dl="$(slx_get dns_listen)"
		[ -n "$dl" ] && echo "dns_listen: \"$dl\""

		eg="$(slx_get egress)"
		[ -n "$eg" ] && echo "egress: \"$eg\""

		# relay 双通道（见 docs/relay-dual-transport.md）
		rt="$(slx_get relay_transport)"
		[ -n "$rt" ] && echo "relay_transport: \"$rt\""

		rp="$(slx_num relay_tls_port)"
		[ -n "$rp" ] && echo "relay_tls_port: $rp"

		rl="$(slx_get relay_tls_listen)"
		[ -n "$rl" ] && echo "relay_tls_listen: \"$rl\""

		rc="$(slx_num relay_tls_conns)"
		[ -n "$rc" ] && echo "relay_tls_conns: $rc"

		# pin 属于凭据类信息：YAML 权限已是 0600，但不要打到日志或状态页。
		rpin="$(slx_get relay_tls_pin)"
		[ -n "$rpin" ] && echo "relay_tls_pin: \"$rpin\""

		ra="$(slx_get relay_advertise)"
		[ -n "$ra" ] && echo "relay_advertise: \"$ra\""

		rsk="$(slx_get relay_clock_skew)"
		[ -n "$rsk" ] && echo "relay_clock_skew: \"$rsk\""

		# 分流开关：只有明确为 1/true 才开。
		# **删掉这个键 = 关闭**（以前只处理 1/0，写成 2 或删掉都不输出，
		# 而 config.go 默认 true ⇒ 「删键」反而把分流打开了）。
		_b="$(slx_bool dispatch)"
		# 没配就是**关闭**（删键即关闭）。
		[ -z "$_b" ] && _b=false
		echo "dispatch: $_b"

		# —— 下面这些以前没有翻译，配了不生效且没有任何提示 ——
		# dispatch_ifaces 最关键：分流接口只能自动识别，
		# 多网桥/旁路由场景挑错接口 ⇒ 真实流量不被标记 ⇒ 境外直连泄露。

		_di="$(slx_list dispatch_ifaces)"
		[ -n "$_di" ] && echo "dispatch_ifaces: [$_di]"

		_lf="$(slx_get log_format)"
		[ -n "$_lf" ] && echo "log_format: \"$_lf\""

		_pp="$(slx_bool slx_pprof)"
		[ -n "$_pp" ] && echo "slx_pprof: $_pp"

		_rtc="$(slx_get relay_tls_cert)"
		[ -n "$_rtc" ] && echo "relay_tls_cert: \"$_rtc\""

		_ct="$(slx_get relay_tls_connect_timeout)"
		[ -n "$_ct" ] && echo "relay_tls_connect_timeout: \"$_ct\""

		_rcd="$(slx_num relay_cert_days)"
		[ -n "$_rcd" ] && echo "relay_cert_days: $_rcd"

		_cnl="$(slx_get cn_list_url)"
		[ -n "$_cnl" ] && echo "cn_list_url: \"$_cnl\""

		_cnls="$(slx_list cn_list_urls)"
		[ -n "$_cnls" ] && echo "cn_list_urls: [$_cnls]"

		_ic="$(slx_get inner_cidr)"
		[ -n "$_ic" ] && echo "inner_cidr: \"$_ic\""

		_tn="$(slx_get tun_name)"
		[ -n "$_tn" ] && echo "tun_name: \"$_tn\""

		_la="$(slx_get listen_addr)"
		[ -n "$_la" ] && echo "listen_addr: \"$_la\""

		_lp="$(slx_num listen_port)"
		[ -n "$_lp" ] && echo "listen_port: $_lp"

		_cp="$(slx_num ctrl_port)"
		[ -n "$_cp" ] && echo "ctrl_port: $_cp"

		_pb="$(slx_num probe_bytes)"
		[ -n "$_pb" ] && echo "probe_bytes: $_pb"

		_ec="$(slx_num egress_capacity)"
		[ -n "$_ec" ] && echo "egress_capacity: $_ec"

		_pa="$(slx_get public_addr)"
		[ -n "$_pa" ] && echo "public_addr: \"$_pa\""

		_pt="$(slx_list probe_targets)"
		[ -n "$_pt" ] && echo "probe_targets: [$_pt]"

		# 探测周期与阈值（数值项要走校验）
		_rpi="$(slx_get relay_tls_probe_interval)"
		[ -n "$_rpi" ] && echo "relay_tls_probe_interval: \"$_rpi\""

		_rpt="$(slx_get relay_probe_timeout)"
		[ -n "$_rpt" ] && echo "relay_probe_timeout: \"$_rpt\""

		_rcf="$(slx_num relay_channel_failures)"
		[ -n "$_rcf" ] && echo "relay_channel_failures: $_rcf"
	} > "$YAML"
	chmod 600 "$YAML"
}

start_service() {
	gen_yaml

	# 出口角色：配置了 gateway_addr 就以 agent 身份跑
	local ga
	ga="$(slx_get gateway_addr)"

	procd_open_instance
	if [ -n "$ga" ]; then
		procd_set_param command "$PROG" agent --config "$YAML"
	else
		procd_set_param command "$PROG" run --config "$YAML"
	fi
	# 崩溃自动重启：3600 秒窗口内最多 5 次，间隔 5 秒
	procd_set_param respawn 3600 5 5
	procd_set_param limits nofile="8192 8192"
	# 日志走 syslog，避免长期运行撑爆闪存
	procd_set_param stdout 1
	procd_set_param stderr 1
	procd_close_instance
}

stop_service() {
	# 优雅退出：slx 会自己撤掉 iptables 规则、ipset 与 TUN
	:
}

service_triggers() {
	procd_add_reload_trigger "slx"
}

reload_service() {
	stop
	start
}
""", 0o755)

# ---------------- LuCI 菜单注册 ----------------
write("/usr/lib/lua/luci/controller/slx.lua", r"""module("luci.controller.slx", package.seeall)

function index()
	if not nixio.fs.access("/etc/config/slx") then
		return
	end

	local page = entry({"admin", "services", "slx"}, firstchild(), _("闪连网关"), 60)
	page.dependent = false
	page.acl_depends = { "luci-app-slx" }

	entry({"admin", "services", "slx", "status"}, template("slx/status"), _("运行状态"), 10)
	entry({"admin", "services", "slx", "config"}, cbi("slx"), _("设置"), 20)

	entry({"admin", "services", "slx", "status_json"}, call("action_status")).leaf = true
end

-- 状态页通过这个接口拉 JSON，避免在 htm 里做复杂逻辑
function action_status()
	local fs = require "nixio.fs"
	local raw = fs.readfile("/tmp/slx-status.json")
	local http = require "luci.http"
	if not raw then
		http.prepare_content("application/json")
		http.write('{"running":false}')
		return
	end
	http.prepare_content("application/json")
	http.write(raw)
end
""")

# ---------------- LuCI 设置页（CBI） ----------------
write("/usr/lib/lua/luci/model/cbi/slx.lua", r"""local m, s, o

m = Map("slx", translate("闪连网关"),
	translate("智能出口网关：境外流量经最快的出口节点转发。修改后需点击「保存并应用」。"))

m.on_after_commit = function(self)
	luci.sys.call("/etc/init.d/slx restart >/dev/null 2>&1")
end

-- ===== 总开关 =====
s = m:section(TypedSection, "slx", translate("基本"))
s.anonymous = true

o = s:option(Flag, "enabled", translate("启用"),
	translate("关闭时服务不启动，所有设置保留。"))
o.rmempty = false

o = s:option(ListValue, "transport_mode", translate("工作模式"),
	translate("proxy：反向连接 + 用户态协议栈（推荐，出口无需 root/TUN）。" ..
		"packet：旧的裸包转发（需要出口有 root 与 TUN，但支持任意 IP 协议）。"))
o:value("proxy", translate("proxy（推荐）"))
o:value("packet", translate("packet（兼容旧模式）"))
o.default = "proxy"

o = s:option(Value, "network_key", translate("网络密钥"),
	translate("32 字节 base64。所有节点必须完全一致；TLS 证书是每次随机生成的自签证书，" ..
		"真正的身份认证靠这个密钥。"))
o.password = true

o = s:option(Value, "egress", translate("强制指定出口"),
	translate("填 node_id 可锁定某个出口；留空则按实测延迟/丢包/带宽自动选路（推荐留空）。"))
o.rmempty = true

-- ===== 本机角色 =====
s = m:section(TypedSection, "slx", translate("本机角色"))
s.anonymous = true

o = s:option(Value, "agent_listen", translate("agent 监听地址（网关角色）"),
	translate("出口节点主动连到这个地址。默认 0.0.0.0:11601，需要放行该 TCP 端口。"))
o.placeholder = "0.0.0.0:11601"
o.rmempty = true

o = s:option(Value, "gateway_addr", translate("网关地址（出口角色）"),
	translate("填了这项，本机就以出口身份运行（slx agent），主动拨这个地址。" ..
		"留空则以网关身份运行（slx run）。家里路由器在 NAT 后面时，这里通常填网关的 ZeroTier 地址。"))
o.placeholder = "192.168.99.1:11601"
o.rmempty = true

-- ===== relay 通道 =====
s = m:section(TypedSection, "slx", translate("relay 通道"))
s.anonymous = true

o = s:option(ListValue, "relay_transport", translate("通道模式"))
o:value("zt", translate("只走 ZeroTier 反向通道（现状，可一键回退）"))
o:value("tls", translate("只走公网直连（网关主动拨 agent 的公网 IP）"))
o:value("auto", translate("自动：按通道评分选，带 8 分滞回"))
o.default = "zt"

o = s:option(Value, "relay_tls_port", translate("公网直连监听端口（出口角色）"),
	translate("agent 侧监听 TLS 的端口。默认 8443 —— agent 上跑着 nginx，443 已被占。" ..
		"nginx 若要代理这个端口，必须用 stream 层 TCP 透传（http 反代会终结 TLS，指纹钉扎失效）。"))
o.placeholder = "8443"
o.rmempty = true

o = s:option(Value, "relay_tls_listen", translate("公网直连监听地址（出口角色）"),
	translate("留空不启用。启用后 agent 监听 TLS 并把公网地址上报给网关，" ..
		"网关据此主动拨过来（网关在 CGNAT 后没有入站）。"))
o.placeholder = "0.0.0.0:8443"
o.rmempty = true

o = s:option(Value, "relay_tls_conns", translate("直连连接数（网关角色）"),
	translate("每个 agent 的并行 TLS 连接数，默认 4。多条独立 TCP 是缓解 TCP-in-TCP 头阻塞的关键。"))
o.placeholder = "4"
o.rmempty = true

o = s:option(Value, "relay_tls_pin", translate("证书指纹钉扎（网关角色，可空）"),
	translate("留空则用 agent 经已认证通道上报的指纹（TOFU）。没有指纹就不会建立直连通道。"))
o.placeholder = "sha256 指纹（64 位 hex）"
o.rmempty = true

o = s:option(Value, "relay_advertise", translate("上报的公网地址（出口角色）"),
	translate("agent 上报给网关的公网 IP:端口。留空则自动探测公网 IP，端口取上面的端口设置。"))
o.placeholder = "1.2.3.4:8443"
o.rmempty = true

o = s:option(Value, "relay_clock_skew", translate("握手时钟偏差上限"),
	translate("默认 60s。实测各节点时钟差 ≤ 8s 且与传输路径无关，一般不用改。"))
o.placeholder = "60s"
o.rmempty = true

-- ===== 分流 =====
s = m:section(TypedSection, "slx", translate("分流"))
s.anonymous = true

o = s:option(Flag, "dispatch", translate("接管境外流量"),
	translate("开启后自动下载中国大陆网段，只把境外流量送进隧道，境内走原路。"))
o.default = "1"
o.rmempty = false

-- ===== DNS =====
s = m:section(TypedSection, "slx", translate("DNS"))
s.anonymous = true

o = s:option(Value, "dns_listen", translate("DNS 转发器监听地址"),
	translate("留空不启用。启用后把路由器 DNS 上游指向这里，境外域名会经出口解析，" ..
		"拿到贴着出口位置的 CDN 地址。例如 127.0.0.1:5353。"))
o.placeholder = "127.0.0.1:5353"
o.rmempty = true

-- ===== 日志 =====
s = m:section(TypedSection, "slx", translate("日志"))
s.anonymous = true

o = s:option(ListValue, "log_level", translate("日志级别"))
o:value("error", "error")
o:value("warn", "warn")
o:value("info", "info")
o:value("debug", "debug")
o.default = "info"

return m
""")

# ---------------- LuCI 状态页（view） ----------------
write("/usr/lib/lua/luci/view/slx/status.htm", r"""<%+header%>

<style>
.slx-card { background:#fff; border-radius:6px; padding:16px; margin-bottom:16px;
            box-shadow:0 1px 3px rgba(0,0,0,.08); }
.slx-card h3 { margin:0 0 12px; font-size:15px; color:#333;
               border-left:3px solid #2196f3; padding-left:8px; }
.slx-kv { display:flex; flex-wrap:wrap; gap:8px 24px; }
.slx-kv div { min-width:150px; }
.slx-kv .k { color:#888; font-size:12px; }
.slx-kv .v { font-size:16px; font-weight:600; color:#222; }
.slx-ok { color:#4caf50; } .slx-bad { color:#f44336; } .slx-warn { color:#ff9800; }
.slx-agent { display:inline-block; background:#e3f2fd; color:#1565c0;
             border-radius:4px; padding:2px 8px; margin:2px; font-size:12px; }
.slx-msg { color:#999; font-size:13px; }
</style>

<div class="cbi-map">
<h2 name="content">闪连网关 · 运行状态</h2>

<div class="slx-card">
	<h3>服务</h3>
	<div class="slx-kv" id="slx-basic">
		<div><div class="k">状态</div><div class="v" id="slx-run">读取中…</div></div>
		<div><div class="k">模式</div><div class="v" id="slx-mode">—</div></div>
		<div><div class="k">运行时长</div><div class="v" id="slx-uptime">—</div></div>
	</div>
</div>

<div class="slx-card">
	<h3>出口</h3>
	<div id="slx-agents" class="slx-msg">读取中…</div>
</div>

<div class="slx-card">
	<h3>分流</h3>
	<div id="slx-split" class="slx-msg">读取中…</div>
</div>

<div class="slx-card">
	<h3>数据面</h3>
	<div class="slx-kv" id="slx-plane">
		<div><div class="k">TUN 收 / 发</div><div class="v" id="slx-tun">—</div></div>
		<div><div class="k">TCP 中继</div><div class="v" id="slx-tcp">—</div></div>
		<div><div class="k">UDP 中继</div><div class="v" id="slx-udp">—</div></div>
		<div><div class="k">ICMP 回复</div><div class="v" id="slx-icmp">—</div></div>
	</div>
</div>

<div class="slx-card">
	<h3>结论</h3>
	<div id="slx-findings" class="slx-msg">读取中…</div>
</div>
</div>

<script type="text/javascript">
//<![CDATA[
function slxAge(sec) {
	if (!sec || sec < 0) return '—';
	var d = Math.floor(sec / 86400), h = Math.floor(sec % 86400 / 3600),
	    m = Math.floor(sec % 3600 / 60);
	if (d > 0) return d + ' 天 ' + h + ' 小时';
	if (h > 0) return h + ' 小时 ' + m + ' 分';
	return m + ' 分钟';
}
function slxSet(id, txt, cls) {
	var el = document.getElementById(id);
	if (!el) return;
	el.textContent = txt;
	el.className = 'v' + (cls ? ' ' + cls : '');
}
function slxRender(d) {
	var running = d.running !== false && d.node_id;
	slxSet('slx-run', running ? '运行中' : '未运行',
		running ? 'slx-ok' : 'slx-bad');
	slxSet('slx-mode', d.mode || '—');
	slxSet('slx-uptime', running && d.started_at
		? slxAge((Date.now() - new Date(d.started_at).getTime()) / 1000) : '—');

	// 出口
	var a = d.agents || {};
	var ag = document.getElementById('slx-agents');
	if (!running) {
		ag.innerHTML = '<span class="slx-msg">服务未运行</span>';
	} else if (!a.count) {
		ag.innerHTML = '<span class="slx-bad">已接入 0 个</span>' +
			'<div class="slx-msg" style="margin-top:6px">出口节点执行：' +
			'<code>slx agent --gateway &lt;本机IP:' +
			(a.listen ? (a.listen.split(':').pop() || '11601') : '11601') + '&gt;</code></div>';
	} else {
		var html = '<span class="slx-ok">已接入 ' + a.count + ' 个</span>' +
			'<span class="slx-msg">　监听 ' + (a.listen || '—') + '</span><div style="margin-top:6px">';
		(a.ids || []).forEach(function(id) {
			html += '<span class="slx-agent">' + id + '</span>';
		});
		ag.innerHTML = html + '</div>';
	}

	// 分流
	var sp = d.split || {};
	var el = document.getElementById('slx-split');
	if (sp.enabled) {
		el.innerHTML = '<span class="slx-ok">已启用</span>' +
			'<span class="slx-msg">　接口 ' + (sp.ifaces || []).join(',') +
			'　国内网段 ' + (sp.cn_entries || 0) + ' 条</span>';
	} else {
		el.innerHTML = '<span class="slx-warn">未启用</span>' +
			'<span class="slx-msg">　境外流量走直连</span>';
	}

	// 数据面
	slxSet('slx-tun', (a.tun_read || 0) + ' / ' + (a.tun_write || 0));
	slxSet('slx-tcp', (a.tcp_accepted || 0) + ' 条');
	slxSet('slx-udp', (a.udp_accepted || 0) + ' 条');
	slxSet('slx-icmp', (a.icmp_replied || 0) + ' 次');

	// 结论
	var f = d.findings || [];
	var fe = document.getElementById('slx-findings');
	if (!running) {
		fe.innerHTML = '<span class="slx-msg">服务未运行，请在「设置」中启用。</span>';
	} else if (!f.length) {
		fe.innerHTML = '<span class="slx-ok">一切正常</span>';
	} else {
		var h = '';
		f.forEach(function(x) {
			var cls = (x.severity === '致命') ? 'slx-bad'
				: (x.severity === '降级' ? 'slx-warn' : 'slx-msg');
			h += '<div style="margin-bottom:8px"><span class="' + cls + '">[' +
				(x.severity || '') + '] ' + (x.title || '') + '</span>';
			if (x.detail) h += '<div class="slx-msg">现象：' + x.detail + '</div>';
			if (x.action) h += '<div class="slx-msg">处理：' + x.action + '</div>';
			h += '</div>';
		});
		fe.innerHTML = h;
	}
}

function slxPoll() {
	var xhr = new XMLHttpRequest();
	xhr.open('GET', '<%=url('admin/services/slx/status_json')%>?_=' + Date.now(), true);
	xhr.onreadystatechange = function() {
		if (xhr.readyState !== 4) return;
		if (xhr.status !== 200) { slxRender({running:false}); return; }
		try { slxRender(JSON.parse(xhr.responseText)); }
		catch (e) { slxRender({running:false}); }
	};
	xhr.send(null);
}

slxPoll();
setInterval(slxPoll, 3000);
//]]>
</script>

<%+footer%>
""")

# ---------------- control ----------------
with open(os.path.join(CTRL, "control"), "w", encoding="utf-8", newline="\n") as f:
    f.write("""Package: luci-app-slx
Version: %s
Depends: libc, luci-base, luci-compat, kmod-tun, ip-full, iptables, ipset
Source: https://github.com/ytw/slx
SourceName: luci-app-slx
License: MIT
Section: luci
SourceDateEpoch: %d
Architecture: %s
Installed-Size: %d
Description: 闪连 SLX 智能出口网关（反向连接 + 用户态协议栈）
 把境外流量经最快的出口节点转发。出口节点不需要 root、TUN 或公网 IP。
 自带 LuCI 管理页面：运行状态与参数配置。
""" % (VERSION, int(time.time()), ARCH, 12000))

# ---------------- postinst / prerm ----------------
write("/etc/init.d/slx", open(os.path.join(DATA, "etc/init.d/slx"), encoding="utf-8").read(), 0o755)

open(os.path.join(CTRL, "postinst"), "w", encoding="utf-8", newline="\n").write("""#!/bin/sh
[ -n "${IPKG_INSTROOT}" ] && exit 0
/etc/init.d/slx enable
/etc/init.d/slx restart >/dev/null 2>&1
rm -f /tmp/luci-indexcache
exit 0
""")

open(os.path.join(CTRL, "prerm"), "w", encoding="utf-8", newline="\n").write("""#!/bin/sh
[ -n "${IPKG_INSTROOT}" ] && exit 0
/etc/init.d/slx stop >/dev/null 2>&1
/etc/init.d/slx disable >/dev/null 2>&1
exit 0
""")

# ---------------- 放二进制 ----------------
bin_dst = os.path.join(DATA, "usr/sbin/slx")
os.makedirs(os.path.dirname(bin_dst), exist_ok=True)
shutil.copyfile(BIN, bin_dst)
os.chmod(bin_dst, 0o755)

# ---------------- 打包 ----------------
def mk_tgz(root, out, base="."):
    with tarfile.open(out, "w:gz", format=tarfile.GNU_FORMAT) as t:
        for dirpath, dirnames, filenames in os.walk(root):
            dirnames.sort(); filenames.sort()
            for name in filenames:
                full = os.path.join(dirpath, name)
                rel = os.path.relpath(full, root).replace("\\", "/")
                # 关键：成员名带 "./" 前缀。opkg 在 control 归档里找的是 "./control"，
                # 不带前缀会被判定为 malformed package。
                ti = t.gettarinfo(full, arcname="./" + rel)
                ti.uid = 0; ti.gid = 0; ti.uname = "root"; ti.gname = "root"
                with open(full, "rb") as fh:
                    t.addfile(ti, fh)


ctrl_tgz = os.path.join(STAGE, "control.tar.gz")
data_tgz = os.path.join(STAGE, "data.tar.gz")
mk_tgz(CTRL, ctrl_tgz)
mk_tgz(DATA, data_tgz)


def ar_pack(out, members):
    """写 ar 归档（.ipk 就是这个格式）。"""
    with open(out, "wb") as f:
        f.write(b"!<arch>\n")
        for name, path in members:
            data = open(path, "rb").read()
            # 用 BSD 风格：文件名不带结尾 "/"，右侧空格补齐到 16 字节。
            # GNU 风格（带 "/"）在本机的 libarchive 上被判定为 malformed。
            arname = name[:16]
            hdr = "{:<16}{:<12}{:<6}{:<6}{:<8}{:<10}`\n".format(
                arname, int(time.time()), 0, 0, "100644", len(data)).encode()
            assert len(hdr) == 60, len(hdr)
            f.write(hdr)
            f.write(data)
            if len(data) % 2 == 1:
                f.write(b"\n")


db = os.path.join(STAGE, "debian-binary")
open(db, "w", newline="\n").write("2.0\n")

ipk = os.path.join(OUTDIR, "luci-app-slx_%s_%s.ipk" % (VERSION, ARCH))
ar_pack(ipk, [("debian-binary", db),
              ("control.tar.gz", ctrl_tgz),
              ("data.tar.gz", data_tgz)])

size = os.path.getsize(ipk)
print("=== 打包完成 ===")
print("  输出: %s" % ipk)
print("  大小: %.2f MB" % (size / 1024 / 1024))
print("  SHA256: %s" % hashlib.sha256(open(ipk, "rb").read()).hexdigest())
print("\n=== 包内容 ===")
for dirpath, dirnames, filenames in os.walk(DATA):
    for name in sorted(filenames):
        full = os.path.join(dirpath, name)
        rel = "/" + os.path.relpath(full, DATA).replace("\\", "/")
        print("  %-46s %8d B" % (rel, os.path.getsize(full)))
for name in sorted(os.listdir(CTRL)):
    print("  [control] %-36s %8d B" % (name, os.path.getsize(os.path.join(CTRL, name))))
