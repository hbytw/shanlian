"""构建 OpenWrt 安装包（tar.gz + 安装脚本）。

放弃 .ipk：opkg 的 ar 解析器对本地（Windows/Python）生成的归档过于挑剔，
而路由器上又没有 ar 工具。改用一个自包含 tar.gz + 安装脚本，
效果相同（一条命令装好），且不依赖任何打包工具链。
"""
import os, sys, io, gzip, tarfile, time, shutil, hashlib

try:
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
except Exception:
    pass

ROOT = r"E:\code\shanlian"
VERSION = "0.10.0-m7"
ARCH = "aarch64"
BIN = os.path.join(ROOT, "dist", "slx-linux-arm64")
OUTDIR = os.path.join(ROOT, "dist")
STAGE = os.path.join(ROOT, ".pkgstage")

if os.path.exists(STAGE):
    shutil.rmtree(STAGE)
os.makedirs(STAGE)


def write(rel, content, mode=0o644):
    p = os.path.join(STAGE, rel.lstrip("/"))
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
\toption agent_labels ''
""")

# ---------------- /etc/init.d/slx（procd） ----------------
write("/etc/init.d/slx", r"""#!/bin/sh /etc/rc.common

START=99
STOP=10
USE_PROCD=1

PROG=/usr/sbin/slx
YAML=/var/etc/slx.yaml

slx_get() {
	uci -q get slx.main."$1" 2>/dev/null
}

# 把 UCI 配置翻译成 slx 读的 YAML。
# 为什么不直接让 slx 读 UCI：slx 要能在非 OpenWrt 平台跑，
# 不能为这个平台引入 uci 依赖。翻译放 init 脚本里，代价最小。
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

		case "$(slx_get dispatch)" in
			1) echo "dispatch: true" ;;
			0) echo "dispatch: false" ;;
		esac
	} > "$YAML"
	chmod 600 "$YAML"
}

# 密钥为空时自动生成并持久化到 UCI。
# 不能让 slx 自己在临时 YAML 里生成 —— /var/etc/slx.yaml 每次启动都会重新生成，
# 那样密钥就每次重启都变，出口永远对不上。
ensure_key() {
	local nk
	nk="$(slx_get network_key)"
	[ -n "$nk" ] && return 0

	if command -v openssl >/dev/null 2>&1; then
		nk="$(openssl rand -base64 32 | tr -d '\n')"
	else
		nk="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
	fi

	uci -q set slx.main.network_key="$nk"
	uci -q commit slx
	logger -t slx "已自动生成网络密钥并写入 /etc/config/slx"
}

start_service() {
	ensure_key
	gen_yaml

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
	# 出口位置标签，格式 "ZT地址=名字,ZT地址=名字"；没配则页面显示 ZT 地址
	local labels
	labels="$(slx_get agent_labels)"
	[ -n "$labels" ] && procd_set_param env SLX_AGENT_LABELS="$labels"
	# 日志走 syslog，避免长期运行撑爆闪存
	procd_set_param stdout 1
	procd_set_param stderr 1
	procd_close_instance
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
	-- 显式 require：不依赖 LuCI 环境里是否有 nixio 全局
	local fs = require "nixio.fs"
	if not fs.access("/etc/config/slx") then
		return
	end

	local page = entry({"admin", "services", "slx"}, firstchild(), _("闪连网关"), 60)
	page.dependent = false
	page.acl_depends = { "luci-app-slx" }

	entry({"admin", "services", "slx", "status"}, template("slx/status"), _("运行状态"), 10)
	entry({"admin", "services", "slx", "config"}, cbi("slx"), _("设置"), 20)
	entry({"admin", "services", "slx", "deploy"}, template("slx/deploy"), _("部署出口"), 30)

	entry({"admin", "services", "slx", "status_json"}, call("action_status")).leaf = true
	entry({"admin", "services", "slx", "agent_script"}, call("action_agent_script")).leaf = true
end

-- 生成出口一键安装脚本：把当前密钥和网关地址填进模板
function action_agent_script()
	local fs   = require "nixio.fs"
	local http = require "luci.http"
	local uci  = require "luci.model.uci".cursor()

	local key = uci:get("slx", "main", "network_key") or ""
	local listen = uci:get("slx", "main", "agent_listen") or "0.0.0.0:11601"
	local port = listen:match(":(%d+)$") or "11601"

	-- 用客户端实际访问的地址作为网关地址：
	-- 出口通过 ZT 访问时这里就是 ZT 地址，通过内网访问时就是内网地址，自动匹配。
	local host = http.getenv("SERVER_ADDR") or ""
	if host == "" then
		host = "192.168.99.1"
	end

	local tpl = fs.readfile("/usr/share/slx/agent-install.sh.tmpl") or ""
	if tpl == "" then
		http.prepare_content("text/plain")
		http.write("错误：模板文件缺失 /usr/share/slx/agent-install.sh.tmpl")
		return
	end

	local gw = host .. ":" .. port
	local body = tpl:gsub("{{KEY}}", key)
	                :gsub("{{GATEWAY}}", gw)
	                :gsub("{{BINBASE}}", "http://" .. host .. "/slx")

	http.prepare_content("text/plain; charset=utf-8")
	http.header("Content-Disposition", 'attachment; filename="slx-install.sh"')
	http.write(body)
end

-- 状态页通过这个接口拉 JSON，避免在模板里做复杂逻辑
function action_status()
	local fs = require "nixio.fs"
	local http = require "luci.http"
	local raw = fs.readfile("/tmp/slx-status.json")
	http.prepare_content("application/json")
	if not raw then
		http.write('{"running":false}')
		return
	end
	http.write(raw)
end
""")

# ---------------- LuCI 设置页（CBI） ----------------
write("/usr/lib/lua/luci/model/cbi/slx.lua", r"""local m, s, o

m = Map("slx", translate("闪连网关"),
	translate("智能出口网关：境外流量经最快的出口节点转发。修改后点「保存并应用」生效。"))

m.on_after_commit = function()
	luci.sys.call("/etc/init.d/slx restart >/dev/null 2>&1")
end

s = m:section(TypedSection, "slx", translate("基本"))
s.anonymous = true

o = s:option(Flag, "enabled", translate("启用"),
	translate("关闭时服务不启动，所有设置保留。"))
o.rmempty = false

o = s:option(ListValue, "transport_mode", translate("工作模式"),
	translate("proxy：反向连接 + 用户态协议栈（推荐，出口无需 root/TUN）。" ..
		"packet：旧的裸包转发（出口需 root 与 TUN，但支持任意 IP 协议）。"))
o:value("proxy", translate("proxy（推荐）"))
o:value("packet", translate("packet（兼容旧模式）"))
o.default = "proxy"

o = s:option(Value, "network_key", translate("网络密钥"),
	translate("32 字节 base64。<strong>所有节点必须完全一致</strong>——" ..
		"把这里的密钥复制到每个出口节点。留空时首次启动会自动生成并保存；" ..
		"TLS 证书每次启动随机生成，真正的身份认证靠这个密钥。"))
o.placeholder = translate("留空则自动生成")
o.rmempty = true

o = s:option(Value, "egress", translate("强制指定出口"),
	translate("填 node_id 可锁定某个出口；留空则按实测延迟/丢包/带宽自动选路（推荐留空）。"))
o.rmempty = true

s = m:section(TypedSection, "slx", translate("本机角色"))
s.anonymous = true

o = s:option(Value, "agent_listen", translate("agent 监听地址（网关角色）"),
	translate("出口节点主动连到这个地址。默认 0.0.0.0:11601，需要放行该 TCP 端口。"))
o.placeholder = "0.0.0.0:11601"
o.rmempty = true

o = s:option(Value, "gateway_addr", translate("网关地址（出口角色）"),
	translate("填了这项，本机就以出口身份运行（slx agent），主动拨这个地址。" ..
		"留空则以网关身份运行（slx run）。家里路由器在 NAT 后面时，通常填网关的 ZeroTier 地址。"))
o.placeholder = "192.168.99.1:11601"
o.rmempty = true

s = m:section(TypedSection, "slx", translate("分流"))
s.anonymous = true

o = s:option(Flag, "dispatch", translate("接管境外流量"),
	translate("开启后自动下载中国大陆网段，只把境外流量送进隧道，境内走原路。"))
o.default = "1"
o.rmempty = false

s = m:section(TypedSection, "slx", translate("DNS"))
s.anonymous = true

o = s:option(Value, "dns_listen", translate("DNS 转发器监听地址"),
	translate("留空不启用。启用后把路由器 DNS 上游指向这里，境外域名会经出口解析，" ..
		"拿到贴着出口位置的 CDN 地址。例如 127.0.0.1:5353。"))
o.placeholder = "127.0.0.1:5353"
o.rmempty = true

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

# ---------------- LuCI 状态页 ----------------
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
			'<span class="slx-msg">　按评分排序，第一行是当前最优</span></div>'+
			'<table class="slx-tbl"><thead><tr>'+
			'<th>位置</th><th>节点</th><th>评分（拆解）</th><th>延迟</th><th>带宽</th><th>丢包</th><th>在线</th><th>状态</th>'+
			'</tr></thead><tbody>';
		a.details.forEach(function(x, idx){
			var state='正常', cls='slx-ok';
			if(x.breaker === '熔断'){ state='熔断'; cls='slx-bad'; }
			else if(x.breaker === '试探'){ state='试探'; cls='slx-warn'; }
			else if(x.eligible === false){ state='不参与'; cls='slx-msg'; }
			else if(x.note){ state=x.note; cls='slx-msg'; }
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
			var stateCell='<td class="'+cls+'">'+state+(tags?'<div>'+tags+'</div>':'')+sub+'</td>';
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
			var rowCls = (idx === 0) ? ' class="slx-best"' : '';
			h += '<tr'+rowCls+'><td>'+loc+(idx===0?' <span class="slx-tag">当前</span>':'')+'</td>'+
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

# ---------------- 部署出口页 ----------------
write("/usr/lib/lua/luci/view/slx/deploy.htm", r"""<%+header%>

<style>
.slx-card{background:#fff;border-radius:6px;padding:16px;margin-bottom:16px;box-shadow:0 1px 3px rgba(0,0,0,.08)}
.slx-card h3{margin:0 0 12px;font-size:15px;color:#333;border-left:3px solid #2196f3;padding-left:8px}
.slx-msg{color:#888;font-size:13px;line-height:1.7}
.slx-cmd{background:#1e1e1e;color:#d4d4d4;border-radius:4px;padding:12px;font-family:monospace;
         font-size:12px;overflow-x:auto;white-space:pre-wrap;word-break:break-all}
.slx-ta{width:100%;height:340px;font-family:monospace;font-size:11px;border:1px solid #ddd;
        border-radius:4px;padding:8px;background:#fafafa}
.slx-btn{display:inline-block;padding:7px 16px;border-radius:4px;background:#2196f3;color:#fff;
         text-decoration:none;font-size:13px;cursor:pointer;border:0}
.slx-btn:hover{background:#1976d2}
.slx-warn{background:#fff3e0;border-left:3px solid #ff9800;padding:10px;border-radius:4px;
          font-size:13px;color:#e65100;margin-bottom:12px}
</style>

<div class="cbi-map">
<h2 name="content">闪连网关 · 部署出口</h2>

<div class="slx-card">
	<h3>第 1 步：确认网关自身可用</h3>
	<div class="slx-msg">
		出口要能连进来，网关必须先「启用」并正在监听。<br>
		去 <a href="<%=url('admin/services/slx/status')%>">运行状态</a> 确认服务是「运行中」。
	</div>
</div>

<div class="slx-card">
	<h3>⚠ 重要：用哪个地址打开本页面</h3>
	<div class="slx-warn">
		生成的脚本里，网关地址取自<strong>你现在访问 LuCI 的地址</strong>。<br><br>
		要给<strong>境外 VPS</strong> 用，必须通过 ZeroTier 地址打开本页面：<br>
		<code>http://192.168.99.1/cgi-bin/luci</code><br><br>
		从内网 <code>192.168.1.254</code> 打开的话，生成的脚本里网关是内网地址，
		VPS 连不上。
	</div>
	<div class="slx-msg" id="gwnow">当前生成的网关地址：<span id="gwwhere">读取中…</span></div>
</div>

<div class="slx-card">
	<h3>第 2 步：在出口 VPS 上执行</h3>
	<div class="slx-msg" style="margin-bottom:8px">
		登录到你的境外 VPS（root），任选一种方式：
	</div>

	<div style="margin-bottom:16px">
		<div class="slx-msg" style="margin-bottom:6px"><strong>方式 A：一条命令</strong>（需要 VPS 能访问到本路由器）</div>
		<div class="slx-cmd" id="oneline">curl -fsSL "http://<%=luci.http.getenv('SERVER_ADDR') or '192.168.99.1'%>/cgi-bin/luci/admin/services/slx/agent_script" | sh</div>
		<div class="slx-msg" style="margin-top:6px">
			注意：这个地址需要登录 Cookie，VPS 直接 curl 会失败。
			推荐用下面两种方式。
		</div>
	</div>

	<div style="margin-bottom:16px">
		<div class="slx-msg" style="margin-bottom:6px"><strong>方式 B：下载后上传</strong>（推荐）</div>
		<a class="slx-btn" href="<%=url('admin/services/slx/agent_script')%>">下载 install.sh</a>
		<div class="slx-msg" style="margin-top:8px">
			下载后传到 VPS：
		</div>
		<div class="slx-cmd">scp install.sh root@&lt;VPS地址&gt;:/tmp/ && ssh root@&lt;VPS地址&gt; "sh /tmp/install.sh"</div>
	</div>

	<div>
		<div class="slx-msg" style="margin-bottom:6px"><strong>方式 C：复制粘贴</strong></div>
		<textarea class="slx-ta" id="scriptbox" readonly>正在生成…</textarea>
		<div class="slx-msg" style="margin-top:6px">
			在 VPS 上执行 <code>vi /tmp/install.sh</code>，粘贴进去，然后 <code>sh /tmp/install.sh</code>
		</div>
	</div>
</div>

<div class="slx-card">
	<h3>脚本会做什么</h3>
	<div class="slx-msg">
		<ol style="margin:0;padding-left:20px">
			<li>自动识别 VPS 架构（amd64 / arm64 / arm / mipsle / riscv64）</li>
			<li>从本路由器下载对应的 slx 程序</li>
			<li>创建非 root 用户 <code>slxagent</code>（出口不需要 root）</li>
			<li>写入配置（<strong>密钥和网关地址已内置</strong>，不用手动填）</li>
			<li>注册 systemd 服务并<strong>设为开机自启</strong></li>
			<li>启动后自动做连接体检，出问题会直接告诉你原因和处理办法</li>
		</ol>
	</div>
</div>

<div class="slx-card">
	<h3>安全提示</h3>
	<div class="slx-warn">
		脚本里含有网络密钥。它相当于整个网络的通行证 —— <strong>只发给自己的机器</strong>，
		不要贴到公开地方。密钥泄露后，任何拿到它的人都能把流量经你的网关转发。
	</div>
	<div class="slx-msg">
		要更换密钥：到 <a href="<%=url('admin/services/slx/config')%>">设置</a> 页清空「网络密钥」并保存，
		系统会重新生成一个。之后所有出口都要用新脚本重装。
	</div>
</div>

</div>

<script type="text/javascript">
//<![CDATA[
(function(){
	var x = new XMLHttpRequest();
	x.open('GET', '<%=url("admin/services/slx/agent_script")%>?_=' + Date.now(), true);
	x.onreadystatechange = function(){
		if (x.readyState !== 4) return;
		var box = document.getElementById('scriptbox');
		var gw  = document.getElementById('gwwhere');
		if (x.status !== 200) {
			box.value = '生成失败（HTTP ' + x.status + '）';
			gw.textContent = '生成失败';
			return;
		}
		box.value = x.responseText;
		var m = x.responseText.match(/^GATEWAY="([^"]+)"/m);
		if (m) {
			gw.textContent = m[1];
			// 内网地址给出醒目提示
			if (/^(10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.)/.test(m[1])) {
				gw.textContent = m[1] + '  ← 这是内网地址，境外 VPS 连不上';
				gw.style.color = '#e65100';
				gw.style.fontWeight = 'bold';
			} else {
				gw.textContent = m[1] + '  ✓ 可用于境外 VPS';
				gw.style.color = '#2e7d32';
			}
		}
	};
	x.send(null);
})();
//]]>
</script>

<%+footer%>
""")

# ---------------- ACL（LuCI 权限） ----------------
write("/usr/share/rpcd/acl.d/luci-app-slx.json", """{
	"luci-app-slx": {
		"description": "Grant access to SLX gateway status",
		"read": {
			"file": {
				"/tmp/slx-status.json": [ "read" ]
			}
		},
		"write": {}
	}
}
""")

# ---------------- 安装脚本 ----------------
write("/install.sh", r"""#!/bin/sh
# 闪连 SLX OpenWrt 安装脚本
set -e

SRC="$(cd "$(dirname "$0")" && pwd)"

echo "==> 检查环境"
[ -x /sbin/procd ] || { echo "错误：不是 OpenWrt 系统"; exit 1; }
[ -e /dev/net/tun ] || echo "警告：/dev/net/tun 不存在，请先 opkg install kmod-tun"

echo "==> 安装二进制"
cp -f "$SRC/usr/sbin/slx" /usr/sbin/slx
chmod 755 /usr/sbin/slx

echo "==> 安装配置（已存在则保留）"
if [ ! -f /etc/config/slx ]; then
	cp -f "$SRC/etc/config/slx" /etc/config/slx
	chmod 600 /etc/config/slx
	echo "    已写入默认配置 /etc/config/slx"
else
	echo "    保留已有配置"
fi

echo "==> 安装服务脚本"
cp -f "$SRC/etc/init.d/slx" /etc/init.d/slx
chmod 755 /etc/init.d/slx

echo "==> 安装部署资源"
mkdir -p /usr/share/slx /www/slx
[ -f "$SRC/usr/share/slx/agent-install.sh.tmpl" ] && \
	cp -f "$SRC/usr/share/slx/agent-install.sh.tmpl" /usr/share/slx/
# 供出口节点下载的二进制（uhttpd 静态服务）
for f in "$SRC"/www/slx/slx-linux-*; do
	[ -f "$f" ] && cp -f "$f" /www/slx/
done
chmod 755 /www/slx 2>/dev/null
chmod 644 /www/slx/* 2>/dev/null

echo "==> 安装 LuCI 页面"
mkdir -p /usr/lib/lua/luci/controller /usr/lib/lua/luci/model/cbi /usr/lib/lua/luci/view/slx
cp -f "$SRC/usr/lib/lua/luci/controller/slx.lua" /usr/lib/lua/luci/controller/
cp -f "$SRC/usr/lib/lua/luci/model/cbi/slx.lua"    /usr/lib/lua/luci/model/cbi/
cp -f "$SRC/usr/lib/lua/luci/view/slx/status.htm"  /usr/lib/lua/luci/view/slx/
cp -f "$SRC/usr/lib/lua/luci/view/slx/deploy.htm"  /usr/lib/lua/luci/view/slx/
mkdir -p /usr/share/rpcd/acl.d
cp -f "$SRC/usr/share/rpcd/acl.d/luci-app-slx.json" /usr/share/rpcd/acl.d/

echo "==> 启用服务"
/etc/init.d/slx enable
rm -f /tmp/luci-indexcache /tmp/luci-modulecache/* 2>/dev/null
/etc/init.d/rpcd reload 2>/dev/null || true

echo ""
echo "安装完成。"
echo ""
echo "下一步："
echo "  1. 打开 LuCI → 服务 → 闪连网关"
echo "  2. 在「设置」填入 network_key（与所有节点一致）"
echo "  3. 勾选「启用」，保存并应用"
echo ""
echo "命令行等价操作："
echo "  uci set slx.main.network_key='<密钥>'"
echo "  uci set slx.main.enabled='1'"
echo "  uci commit slx && /etc/init.d/slx restart"
""", 0o755)

# ---------------- 安装脚本模板 ----------------
tpl_src = os.path.join(ROOT, "scripts", "templates", "slx-agent-install.sh")
tpl_dst = os.path.join(STAGE, "usr/share/slx/agent-install.sh.tmpl")
os.makedirs(os.path.dirname(tpl_dst), exist_ok=True)
with open(tpl_src, encoding="utf-8") as f:
    tpl = f.read().replace("\r\n", "\n")
with open(tpl_dst, "w", encoding="utf-8", newline="\n") as f:
    f.write(tpl)
os.chmod(tpl_dst, 0o644)
print("  模板: %d 字节" % len(tpl))

# ---------------- 放二进制 ----------------
bin_dst = os.path.join(STAGE, "usr/sbin/slx")
os.makedirs(os.path.dirname(bin_dst), exist_ok=True)
shutil.copyfile(BIN, bin_dst)
os.chmod(bin_dst, 0o755)

# 供出口节点下载用的二进制（放在 /www 下，uhttpd 静态服务，无需登录）
# 只放 VPS 最常用的两种架构，控制包体积
www_dir = os.path.join(STAGE, "www/slx")
os.makedirs(www_dir, exist_ok=True)
for arc in ("amd64", "arm64"):
    src = os.path.join(ROOT, "dist", "slx-linux-" + arc)
    if os.path.exists(src):
        shutil.copyfile(src, os.path.join(www_dir, "slx-linux-" + arc))
        os.chmod(os.path.join(www_dir, "slx-linux-" + arc), 0o644)
        print("  下载用: slx-linux-%s" % arc)

# ---------------- 打包 ----------------
out = os.path.join(OUTDIR, "slx-openwrt-%s-%s.tar.gz" % (VERSION, ARCH))
with tarfile.open(out, "w:gz", format=tarfile.GNU_FORMAT) as t:
    for dirpath, dirnames, filenames in os.walk(STAGE):
        dirnames.sort()
        filenames.sort()
        for name in filenames:
            full = os.path.join(dirpath, name)
            rel = os.path.relpath(full, STAGE).replace("\\", "/")
            ti = t.gettarinfo(full, arcname=rel)
            ti.uid = 0
            ti.gid = 0
            ti.uname = "root"
            ti.gname = "root"
            with open(full, "rb") as fh:
                t.addfile(ti, fh)

size = os.path.getsize(out)
print("=== 打包完成 ===")
print("  输出: %s" % out)
print("  大小: %.2f MB" % (size / 1024 / 1024))
print("  SHA256: %s" % hashlib.sha256(open(out, "rb").read()).hexdigest())
print("\n=== 包内容 ===")
for dirpath, dirnames, filenames in os.walk(STAGE):
    for name in sorted(filenames):
        full = os.path.join(dirpath, name)
        rel = "/" + os.path.relpath(full, STAGE).replace("\\", "/")
        print("  %-52s %8d B" % (rel, os.path.getsize(full)))
