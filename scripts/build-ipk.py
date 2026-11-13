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
.slx-card{background:#fff;border-radius:6px;padding:16px;margin-bottom:16px;box-shadow:0 1px 3px rgba(0,0,0,.08)}
.slx-card h3{margin:0 0 12px;font-size:15px;color:#333;border-left:3px solid #2196f3;padding-left:8px}
.slx-kv{display:flex;flex-wrap:wrap;gap:8px 24px}
.slx-kv>div{min-width:140px}
.slx-kv .k{color:#888;font-size:12px}
.slx-kv .v{font-size:16px;font-weight:600;color:#222}
.slx-ok{color:#4caf50}.slx-bad{color:#f44336}.slx-warn{color:#ff9800}
.slx-agent{display:inline-block;background:#e3f2fd;color:#1565c0;border-radius:4px;padding:2px 8px;margin:2px;font-size:12px}
.slx-msg{color:#999;font-size:13px}
.slx-tbl{width:100%;border-collapse:collapse;font-size:13px}
.slx-tbl th{text-align:left;padding:6px 8px;color:#888;font-weight:600;border-bottom:2px solid #eee;font-size:12px}
.slx-tbl td{padding:6px 8px;border-bottom:1px solid #f5f5f5}
.slx-tbl tr:last-child td{border-bottom:0}
.slx-mono{font-family:monospace;color:#555}
.slx-best{background:#f1f8e9}
.slx-tag{display:inline-block;background:#4caf50;color:#fff;border-radius:3px;padding:1px 5px;font-size:10px;margin-left:4px;vertical-align:middle}
.slx-sub{font-size:11px;color:#999;margin-top:2px;line-height:1.4}
.slx-mini{display:inline-block;background:#f5f5f5;color:#666;border-radius:3px;padding:0 5px;font-size:10px;margin:2px 4px 0 0}
</style>

<div class="cbi-map">
<h2 name="content">闪连网关 · 运行状态</h2>

<div class="slx-card">
	<h3>服务</h3>
	<div class="slx-kv">
		<div><div class="k">状态</div><div class="v" id="s-run">读取中…</div></div>
		<div><div class="k">模式</div><div class="v" id="s-mode">—</div></div>
		<div><div class="k">运行时长</div><div class="v" id="s-up">—</div></div>
		<div><div class="k">版本</div><div class="v" id="s-ver">—</div></div>
		<div><div class="k">数据更新</div><div class="v" id="s-fresh">—</div></div>
		<div><div class="k">节点 ID</div><div class="v" id="s-node">—</div></div>
	</div>
</div>

<div class="slx-card"><h3>出口</h3><div id="s-agents" class="slx-msg">读取中…</div></div>

<div class="slx-card"><h3>通道（直连 / 兜底）</h3><div id="s-channels" class="slx-msg">读取中…</div></div>

<div class="slx-card"><h3>分流</h3><div id="s-split" class="slx-msg">读取中…</div></div>

<div class="slx-card"><h3>DNS 转发</h3><div id="s-dns" class="slx-msg">读取中…</div></div>

<div class="slx-card">
	<h3>流量 · 错误</h3>
	<div class="slx-sub" style="margin-bottom:8px">实时 = 最近一次刷新的增量速率（约 3 秒粒度）；合计与错误 = 自服务启动以来累计。</div>
	<div class="slx-kv" style="margin-bottom:10px">
		<div><div class="k">实时 TCP 吞吐</div><div class="v" id="r-tcp">—</div></div>
		<div><div class="k">实时 UDP 吞吐</div><div class="v" id="r-udp">—</div></div>
		<div><div class="k">实时 TUN 收包 / 发包</div><div class="v" id="r-tun">—</div></div>
	</div>
	<div class="slx-kv" style="margin-bottom:10px">
		<div><div class="k">TCP 成功 / 失败</div><div class="v" id="s-tcp">—</div></div>
		<div><div class="k">失败细分（连不上/栈/超限）</div><div class="v" id="s-tcpfail">—</div></div>
		<div><div class="k">在途 TCP 连接</div><div class="v" id="s-tcpactive">—</div></div>
		<div><div class="k">UDP 成功 / 失败</div><div class="v" id="s-udp">—</div></div>
		<div><div class="k">ICMP 回复 / 丢弃</div><div class="v" id="s-icmp">—</div></div>
		<div><div class="k">TUN 收 / 发（包）</div><div class="v" id="s-tun">—</div></div>
	</div>
	<div class="slx-kv">
		<div><div class="k">合计流量 TCP / UDP</div><div class="v" id="s-bytes">—</div></div>
		<div><div class="k">流量总计</div><div class="v" id="s-bytestotal">—</div></div>
		<div><div class="k">错误总计</div><div class="v" id="s-errtotal">—</div></div>
		<div style="min-width:340px"><div class="k">错误明细</div><div class="v" id="s-errdetail" style="font-size:12px;font-weight:400;line-height:1.6">—</div></div>
	</div>
</div>

<div class="slx-card"><h3>运行环境</h3><div id="s-env" class="slx-msg">读取中…</div></div>

<div class="slx-card"><h3>结论</h3><div id="s-find" class="slx-msg">读取中…</div></div>
</div>

<script type="text/javascript">
//<![CDATA[
function slxAge(sec){
	if(!sec||sec<0)return '—';
	var d=Math.floor(sec/86400),h=Math.floor(sec%86400/3600),m=Math.floor(sec%3600/60);
	if(d>0)return d+' 天 '+h+' 小时';
	if(h>0)return h+' 小时 '+m+' 分';
	return m+' 分钟';
}
// 实时速率的跨次采样状态（字节累计值 + 上次采样时刻）
var _slxRate={t:0,tcp:0,udp:0,tr:0,tw:0,tcpNow:'',udpNow:'',tunNow:''};
function slxSet(id,txt,cls){
	var el=document.getElementById(id); if(!el)return;
	el.textContent=txt; el.className='v'+(cls?' '+cls:'');
}
function slxRender(d){
	var run = d.running!==false && d.node_id;
	slxSet('s-run', run?'运行中':'未运行', run?'slx-ok':'slx-bad');
	slxSet('s-mode', d.mode||'—');
	slxSet('s-up', (run&&d.started_at)?slxAge((Date.now()-new Date(d.started_at).getTime())/1000):'—');
	slxSet('s-ver', d.version||'—');
	slxSet('s-node', d.node_id?d.node_id.substring(0,12):'—');
	// 数据新鲜度：快照每 5 秒写一次，超过 30 秒没更新说明守护进程卡了
	if(d.updated_at){
		var age=Math.round((Date.now()-new Date(d.updated_at).getTime())/1000);
		var cls = age<30?'slx-ok':(age<120?'slx-warn':'slx-bad');
		slxSet('s-fresh', age<60?(age+' 秒前'):(Math.round(age/60)+' 分钟前'), cls);
	}else{ slxSet('s-fresh','—'); }

	// —— 实时速率：用相邻两次快照的字节差 ÷ 时间差（页面 3 秒拉一次）——
	var a=d.agents||{};
	if(a.tcp_bytes!==undefined){
		var now=Date.now();
		if(_slxRate.t){
			var dt=(now-_slxRate.t)/1000;
			if(dt>=2){   // 至少隔 2 秒才算，避免快照未更新时算出 0
				var dT=(a.tcp_bytes||0)-_slxRate.tcp; if(dT<0)dT=0;
				var dU=(a.udp_bytes||0)-_slxRate.udp; if(dU<0)dU=0;
				var dR=(a.tun_read||0)-_slxRate.tr;   if(dR<0)dR=0;
				var dW=(a.tun_write||0)-_slxRate.tw;  if(dW<0)dW=0;
				_slxRate.tcpNow=(dT*8/dt/1e6).toFixed(2)+' Mbps';
				_slxRate.udpNow=(dU*8/dt/1e6).toFixed(2)+' Mbps';
				_slxRate.tunNow=Math.round(dR/dt)+' / '+Math.round(dW/dt)+' 包/秒';
				_slxRate.t=now; _slxRate.tcp=a.tcp_bytes||0; _slxRate.udp=a.udp_bytes||0;
				_slxRate.tr=a.tun_read||0; _slxRate.tw=a.tun_write||0;
			}
		}else{
			_slxRate.t=now; _slxRate.tcp=a.tcp_bytes||0; _slxRate.udp=a.udp_bytes||0;
			_slxRate.tr=a.tun_read||0; _slxRate.tw=a.tun_write||0;
		}
		slxSet('r-tcp', _slxRate.tcpNow||'采样中…');
		slxSet('r-udp', _slxRate.udpNow||'采样中…');
		slxSet('r-tun', _slxRate.tunNow||'采样中…');
	}

	var a=d.agents||{}, ag=document.getElementById('s-agents');
	if(!run){ ag.innerHTML='<span class="slx-msg">服务未运行</span>'; }
	else if(!a.count){
		ag.innerHTML='<span class="slx-bad">已接入 0 个</span>'+
			'<div class="slx-msg" style="margin-top:6px">在出口节点执行：'+
			'<code>slx agent --gateway &lt;本机IP:11601&gt;</code></div>';
	}else if(a.details && a.details.length){
		var h='<div style="margin-bottom:8px"><span class="slx-ok">已接入 '+a.count+' 个</span>'+
			'<span class="slx-msg">　监听 '+(a.listen||'—')+'</span>'+
			'<span class="slx-msg">　按评分排序，第一个「当前」是实际在用的出口</span></div>'+
			'<table class="slx-tbl"><thead><tr>'+
			'<th>位置</th><th>节点</th><th>评分（拆解）</th><th>延迟</th><th>带宽</th><th>丢包</th><th>在线</th><th>状态</th>'+
			'</tr></thead><tbody>';
		// 「当前」= 第一个有资格参与选路的出口（熔断的即使分高也不是它在扛流量）
		var curIdx=-1;
		a.details.forEach(function(x, idx){
			if(curIdx<0 && x.eligible!==false && x.breaker!=='熔断') curIdx=idx;
		});
		a.details.forEach(function(x, idx){
			var state='正常', cls='slx-ok', noteSub='';
			if(x.breaker === '熔断'){ state='熔断'; cls='slx-bad'; if(x.note) noteSub='<div class="slx-sub slx-msg">'+x.note+'</div>'; }
			else if(x.breaker === '试探'){ state='试探'; cls='slx-warn'; if(x.note) noteSub='<div class="slx-sub slx-msg">'+x.note+'</div>'; }
			else if(x.eligible === false){ state='不参与'; cls='slx-msg'; }
			else if(x.note){
				// 正常出口的失败留痕：不顶替「正常」，降为下方灰色小字 ——
				// 顶替会让全绿的出口看着像还在失败（2026-11-13 伟哥困惑过）
				noteSub='<div class="slx-sub slx-msg">'+x.note+'</div>';
			}
			var rtt = x.rtt_ms > 0 ? Math.round(x.rtt_ms)+' ms' : '—';
			var bw  = x.bw_mbps > 0 ? x.bw_mbps.toFixed(1)+' M' : '—';
			var loss= (x.loss > 0) ? (x.loss*100).toFixed(1)+'%' : '0%';
			var loc = x.label || x.remote || '—';
			// 状态的第二行：失败归因（熔断/失败原因 + 计数）
			var sub='';
			if(x.breaker_reason) sub+='<div class="slx-sub slx-warn">'+x.breaker_reason+'</div>';
			var tags='';
			if(x.target_failures>0) tags+='<span class="slx-mini">目标打不开 '+x.target_failures+'</span>';
			if(x.link_failures>0) tags+='<span class="slx-mini">链路失败 '+x.link_failures+'</span>';
			if(x.link_reason && x.link_failures>0) sub+='<div class="slx-sub slx-msg">'+x.link_reason+'</div>';
			var stateCell='<td class="'+cls+'">'+state+(tags?'<div>'+tags+'</div>':'')+noteSub+sub+'</td>';
			// 评分拆解：三维度贡献 + 亲和
			var parts='<span class="slx-mini">带宽 '+(x.score_bw||0).toFixed(1)+'</span>'+
			          '<span class="slx-mini">延迟 '+(x.score_rtt||0).toFixed(1)+'</span>'+
			          '<span class="slx-mini">丢包 '+(x.score_loss||0).toFixed(1)+'</span>'+
			          (x.affinity?'<span class="slx-mini">亲和+15</span>':'');
			var scoreCell='<td><strong>'+x.score.toFixed(1)+'</strong><div>'+parts+'</div></td>';
			var up='—';
			if(x.uptime_seconds>0){
				up = x.uptime_seconds>=86400 ? Math.floor(x.uptime_seconds/86400)+'天'
				   : x.uptime_seconds>=3600 ? Math.floor(x.uptime_seconds/3600)+'时'
				   : Math.floor(x.uptime_seconds/60)+'分';
			}
			var rowCls = (idx === curIdx) ? ' class="slx-best"' : '';
			h += '<tr'+rowCls+'><td>'+loc+(idx===curIdx?' <span class="slx-tag">当前</span>':'')+'</td>'+
			     '<td class="slx-mono">'+x.id+'</td>'+
			     scoreCell+
			     '<td>'+rtt+'</td><td>'+bw+'</td><td>'+loss+'</td><td>'+up+'</td>'+
			     stateCell+'</tr>';
		});
		ag.innerHTML = h + '</tbody></table>'+
			'<div class="slx-msg" style="margin-top:8px;line-height:1.7">'+
			'<b>评分公式</b>：总分 = 100×(0.45×带宽分 + 0.35×延迟分 + 0.20×丢包分)，域名亲和 +15，上限 100。<br>'+
			'带宽分 = bw/(bw+50Mbps)（软上限 50M）；延迟分 = 100/(100+延迟ms)；丢包分 = 1−丢包率。<br>'+
			'<b>为什么普遍 30-40 分</b>：丢包 0% 拿满 20 分；跨境延迟 100-600ms 只拿 9-18 分；'+
			'带宽是被动统计（通常几 Mbps）只拿 1-4 分。分数看相对不看绝对 —— 选路只比谁高。<br>'+
			'评分越高越优先；延迟/带宽/丢包为实时实测值。</div>';
	}else{
		var h='<span class="slx-ok">已接入 '+a.count+' 个</span>'+
			'<span class="slx-msg">　监听 '+(a.listen||'—')+'</span><div style="margin-top:6px">';
		(a.ids||[]).forEach(function(id){ h+='<span class="slx-agent">'+id+'</span> '; });
		ag.innerHTML=h+'<div class="slx-msg" style="margin-top:6px">测量数据生成中…</div></div>';
	}

	var sp=d.split||{}, el=document.getElementById('s-split');
	if(sp.enabled){
		el.innerHTML='<span class="slx-ok">已启用</span><span class="slx-msg">　接口 '+
			(sp.ifaces||[]).join(',')+'　国内网段 '+(sp.cn_entries||0)+' 条</span>';
	}else{
		el.innerHTML='<span class="slx-warn">未启用</span><span class="slx-msg">　境外流量走直连</span>';
	}

	slxSet('s-tun',(a.tun_read||0)+' / '+(a.tun_write||0));
	slxSet('s-tcp',(a.tcp_accepted||0)+' / '+(a.tcp_failed||0));
	slxSet('s-tcpfail',(a.tcp_fail_connect||0)+' / '+(a.tcp_fail_endpoint||0)+' / '+(a.tcp_fail_limit||0));
	slxSet('s-tcpactive',String(a.tcp_active||0));
	function slxFmtBytes(n){
		if(!n||n<=0) return '0';
		if(n>=1073741824) return (n/1073741824).toFixed(1)+' GB';
		if(n>=1048576) return (n/1048576).toFixed(1)+' MB';
		if(n>=1024) return (n/1024).toFixed(0)+' KB';
		return n+' B';
	}
	var tcpB=a.tcp_bytes||0, udpB=a.udp_bytes||0;
	slxSet('s-bytes', slxFmtBytes(tcpB)+' / '+slxFmtBytes(udpB));
	slxSet('s-bytestotal', slxFmtBytes(tcpB+udpB));
	slxSet('s-udp',(a.udp_accepted||0)+' / '+(a.udp_failed||0));
	slxSet('s-icmp',(a.icmp_replied||0)+' / '+(a.icmp_dropped||0));

	// —— 错误汇总：所有可计数的错误加一个总数，明细一行列出 ——
	var dnsF=(d.dns&&d.dns.failures)||0;
	var linkF=0; (a.details||[]).forEach(function(x){linkF+=(x.link_failures||0);});
	var chBreak=0; (d.channels||[]).forEach(function(c){ if(c.open) chBreak++; });
	var errTotal=(a.tcp_failed||0)+(a.udp_failed||0)+dnsF+linkF+chBreak;
	slxSet('s-errtotal', String(errTotal), errTotal>0?'slx-warn':'slx-ok');
	slxSet('s-errdetail',
		'TCP 失败 '+((a.tcp_failed||0))+
		'（连不上 '+(a.tcp_fail_connect||0)+' / 协议栈 '+(a.tcp_fail_endpoint||0)+' / 超限 '+(a.tcp_fail_limit||0)+'）'+
		'　UDP 失败 '+(a.udp_failed||0)+
		'　DNS 失败 '+dnsF+
		'　链路失败 '+linkF+
		'　通道熔断 '+chBreak);

	// —— 运行环境 ——
	var ev=d.env||{}, eel=document.getElementById('s-env');
	if(eel){
		eel.innerHTML='<div class="slx-kv">'+
			'<div><div class="k">默认路由接口</div><div class="v">'+(ev.default_iface||'—')+'</div></div>'+
			'<div><div class="k">LAN 接口</div><div class="v">'+(ev.lan_iface||'—')+'</div></div>'+
			'<div><div class="k">ZeroTier 接口</div><div class="v">'+((ev.zt_ifaces&&ev.zt_ifaces.length)?ev.zt_ifaces.join(', '):'无')+'</div></div>'+
			'<div><div class="k">防火墙后端</div><div class="v">'+(ev.firewall||'—')+'</div></div>'+
			'<div><div class="k">ipset 分流</div><div class="v '+(ev.has_ipset?'slx-ok':'slx-bad')+'">'+(ev.has_ipset?'可用':'缺失')+'</div></div>'+
			'<div><div class="k">TUN 设备</div><div class="v '+(ev.has_tun?'slx-ok':'slx-bad')+'">'+(ev.has_tun?'已创建':'未创建')+'</div></div>'+
			'</div>';
	}

	// —— 通道卡 ——
	var chs=d.channels||[], cel=document.getElementById('s-channels');
	if(!run){ cel.innerHTML='<span class="slx-msg">服务未运行</span>'; }
	else if(!chs.length){ cel.innerHTML='<span class="slx-msg">暂无通道数据（服务刚启动，等下一轮刷新）</span>'; }
	else{
		// 节点 ID → 位置标签 的映射（通道里是完整 node_id，详情里是截短的）
		var lbl={};
		(a.details||[]).forEach(function(x){ if(x.label) lbl[x.id]=x.label; });
		var h='<table class="slx-tbl"><thead><tr>'+
			'<th>位置</th><th>出口</th><th>类型</th><th>对端</th><th>评分</th>'+
			'<th>RTT</th><th>成功率</th><th>吞吐</th>'+
			'<th>角色</th><th>状态</th><th>流数</th><th>在线</th>'+
			'</tr></thead><tbody>';
		chs.forEach(function(c){
			var nid=c.node_id||'';
			var short=nid.substring(0,12);
			var loc=lbl[short]||lbl[nid]||short;
			var kind = c.kind==='tls'?'<span class="slx-ok">直连</span>':'<span class="slx-msg">兜底</span>';
			var role = c.primary?'<span class="slx-tag">主</span>':'备';
			var st = c.open?'<span class="slx-bad">熔断</span>':'<span class="slx-ok">正常</span>';
			var streams = (c.streams>50)?'<span class="slx-warn">'+c.streams+'</span>':(c.streams||0);
			var rtt = (c.rtt_ms>0)?Math.round(c.rtt_ms)+' ms':'—';
			// 成功率低标黄（<95%），吞吐正常显示
			var sr = (c.succ!==undefined && c.succ>0)
				? ((c.succ*100).toFixed(1)+'%'+(c.succ<0.95?' <span class="slx-warn">↓</span>':''))
				:'—';
			var bwv = (c.bw_mbps>0)?(c.bw_mbps.toFixed(1)+' M'):'—';
			h+='<tr><td>'+loc+'</td><td class="slx-mono">'+short+'</td><td>'+kind+'</td>'+
			   '<td class="slx-mono">'+(c.remote||'—')+'</td>'+
			   '<td><strong>'+Number(c.score||0).toFixed(1)+'</strong></td>'+
			   '<td>'+rtt+'</td><td>'+sr+'</td><td>'+bwv+'</td>'+
			   '<td>'+role+'</td><td>'+st+'</td><td>'+streams+'</td>'+
			   '<td>'+(c.since?slxAge((Date.now()-new Date(c.since).getTime())/1000):'—')+'</td></tr>';
		});
		cel.innerHTML=h+'</tbody></table>'+
			'<div class="slx-msg" style="margin-top:8px">直连=公网 TCP 直连通道；兜底=ZeroTier 隧道。'+
			'RTT/成功率/吞吐为该通道探测 EWMA（无样本时兜底 200ms / 50% / 5M）。流数持续偏高说明有开流卡住。</div>';
	}

	// —— DNS 卡 ——
	var dn=d.dns||{}, del=document.getElementById('s-dns');
	if(!dn.enabled){
		del.innerHTML='<span class="slx-warn">未启用</span><span class="slx-msg">　境外域名不解析</span>';
	}else{
		var failCls = dn.failures>0 && dn.queries>0 && (dn.failures/dn.queries>0.3)?'slx-warn':'slx-msg';
		del.innerHTML='<div class="slx-kv">'+
			'<div><div class="k">监听</div><div class="v">'+(dn.addr||'—')+'</div></div>'+
			'<div><div class="k">查询总数</div><div class="v">'+(dn.queries||0)+'</div></div>'+
			'<div><div class="k">本地 / 经出口</div><div class="v">'+(dn.local_hits||0)+' / '+(dn.egress_hits||0)+'</div></div>'+
			'<div><div class="k">失败</div><div class="v '+failCls+'">'+(dn.failures||0)+'</div></div>'+
			'<div><div class="k">缓存 命中/未命中</div><div class="v">'+(dn.cache_hits||0)+' / '+(dn.cache_misses||0)+'</div></div>'+
			'<div><div class="k">缓存条目</div><div class="v">'+(dn.cache_entries||0)+'</div></div>'+
			'</div>';
	}

	var f=d.findings||[], fe=document.getElementById('s-find');
	if(!run){ fe.innerHTML='<span class="slx-msg">服务未运行，请在「设置」中启用。</span>'; }
	else if(!f.length){ fe.innerHTML='<span class="slx-ok">一切正常</span>'; }
	else{
		var h='';
		f.forEach(function(x){
			var cls=(x.severity==='致命')?'slx-bad':(x.severity==='降级'?'slx-warn':'slx-msg');
			h+='<div style="margin-bottom:8px"><span class="'+cls+'">['+(x.severity||'')+'] '+
				(x.title||'')+'</span>';
			if(x.detail)h+='<div class="slx-msg">现象：'+x.detail+'</div>';
			if(x.action)h+='<div class="slx-msg">处理：'+x.action+'</div>';
			h+='</div>';
		});
		fe.innerHTML=h;
	}
}
function slxPoll(){
	var x=new XMLHttpRequest();
	x.open('GET','<%=url('admin/services/slx/status_json')%>?_='+Date.now(),true);
	x.onreadystatechange=function(){
		if(x.readyState!==4)return;
		if(x.status!==200){slxRender({running:false});return;}
		try{slxRender(JSON.parse(x.responseText));}catch(e){slxRender({running:false});}
	};
	x.send(null);
}
slxPoll(); setInterval(slxPoll,3000);
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
