# SLX

自建代理网关。**家用 OpenWrt 路由器做网关**，**境外 VPS 做出口节点**，一条命令接管全家流量。

```
家里所有设备 ──▶ OpenWrt (192.168.1.254)
                  │
                  ├─ 国内网段 ──────────────────────▶ 直连 ✅
                  │
                  └─ 其余流量 ──▶ TUN (用户态协议栈)
                                    │
                                    ▼
                              SLX 网关进程
                                    │
                          ┌─────────┴─────────┐
                          │                   │
                     直连通道              ZT 通道
                  agent 公网 :8443        ZeroTier 虚拟网
                     (TLS + yamux)          (TCP + yamux)
                          │                   │
                          └─────────┬─────────┘
                                    ▼
                            境外 agent 节点
                          (net.Dial 到真实目标)
```

## 双通道设计

| 通道 | 路径 | 用途 |
|---|---|---|
| 直连 | 网关 → agent 公网 IP :8443（TLS） | 主用。跨境 UDP 被限流时更稳 |
| ZT | ZeroTier 虚拟网卡 (192.168.99.0/24) | 兜底。直连被封时接管 |

网关按目标地址哈希在可用通道间打散，单条通道故障只影响它自己承载的流。

**注意**：两条路共享同一条家宽出口。家宽抖动时双通道会同时失效——这是架构天花板，不是 bug。

## 关键设计

**用户态协议栈终结连接**。SLX 不做内核转发，而是在 TUN 里跑一个 gvisor netstack，连接终结在用户态，然后通过 relay 通道送到 agent。

- `slx0` **故意不配 IPv4 地址**（只设 MTU + up）——ZeroTier 会绑定所有带地址的接口并把它当源地址
- 内核只做两件事：策略路由把标记过的包送进 TUN、把应答包送回 LAN
- 分流打标在 mangle PREROUTING：`! slx_cn && ! slx_exempt → MARK 0x100`

**熔断只能由链路探测判定**。业务请求失败（目标打不开）不计入熔断，否则会形成自喂循环：业务失败 → 熔断 → 无可用出口 → 业务更失败。

**DNS 默认走出口解析**（防泄露）。国内域名用 dnsmasq 按域名列表定向到家宽 DNS，避免拿到境外 CDN 节点被误判为境外流量。

## 部署

### 网关（OpenWrt）

```sh
# init 脚本由 UCI 生成 YAML ✅✅
/etc/init.d/slx enable
/etc/init.d/slx start
```

关键 UCI 配置：

| 键 | 默认 | 说明 |
|---|---|---|
| `transport_mode` | proxy | proxy = 用户态栈；packet = 内核转发 |
| `relay_transport` | auto | auto / tls / zt |
| `relay_tls_conns` | 4 | 每 agent 的直连通道数 |
| `relay_tls_probe_interval` | 15s | 通道探测周期 |
| `relay_probe_timeout` | 10s | 单次探测超时 |
| `relay_channel_failures` | 5 | 连续失败几次熔断 |
| `dispatch` | 1 | 是否启用分流 |
| `dns_listen` | 127.0.0.1:5335 | DNS 转发器监听 |
| `agent_labels` | - | `IP=名字` 逗号分隔，用于状态页显示 |

### 出口节点（Linux）

```sh
scp slx-linux-amd64 root@<host>:/usr/local/bin/slx
# 配置 /etc/slx/config.yaml ✅✅
gateway_addr: "192.168.99.1:11601"
relay_tls_listen: "0.0.0.0:8443"
relay_advertise: "<你的公网IP>"     # 只填 IP，不带端口 ✅✅
relay_tls_cert: "/var/lib/slx/relay-cert.pem"
```

`relay_advertise` **必须是纯主机名/IP**——带端口会被 `net.JoinHostPort` 加方括号导致 DNS 解析失败。

证书目录要在 `slxagent` 用户下可写（agent 不是 root 跑）。

## 构建

```sh
go build ./cmd/slx

# 交叉编译 ✅✅
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o slx-linux-arm64 ./cmd/slx
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o slx-linux-amd64 ./cmd/slx
```

## CI

每次 push 都会跑：

- `go build` / `go vet`
- **`go test -race`** ← 硬门槛
- `go test`
- 交叉编译（linux/amd64、linux/arm64、darwin/arm64、windows/amd64）

竞态检测是硬门槛。2026-11-11 有两个生产事故（`concurrent map writes` panic、relay 的 DATA RACE）都是 `-race` 能提前抓到的，而当时没人在上机前跑。

## 目录结构

```
cmd/slx/              入口
internal/
  cli/                命令行、代理平面、分流看门狗
  config/             配置结构与 UCI 兼容
  netstack/           gvisor 用户态协议栈
  stream/             帧协议与流复用
  relay/              网关与 agent 的通道管理
  dispatch/           分流规则（ipset / 打标 / 路由表）
  netcfg/             iptables / ipset / 路由表操作
  dnsfwd/             DNS 转发器
  cnlist/             国内网段表
  picker/             出口选路与评分
```

## 已知限制

- **双通道共享家宽出口**——家宽抖动时两条路会同时失效
- **zt 模式下每 agent 只有一条 TCP**——TCP-in-TCP 头阻塞，吞吐受限（`auto` 模式可缓解）
- **`dispatch_ifaces` 靠自动识别**——多网桥/旁路由场景可能挑错接口（可显式配置）
- **`/etc/firewall.user` 若用 `iptables -I` 不幂等**——每次防火墙重载会累积重复规则
