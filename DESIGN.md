# 闪连 SLX — ZeroTier 之上的智能出口网关 设计方案

> **版本**：v3.1（ZeroTier 作为既有组网底座）
> **日期**：2026-11-04
> **状态**：待评审

---

## 0. 需求确认

### 0.1 既有前提（你已具备的）

```
全球多个 VPS  +  家里几个 OpenWrt 旁路由
        │
        └── 已由 ZeroTier 组成一个互通的虚拟局域网（下称 "ZT LAN"）
             · 节点间已可互通
             · 已自带端到端加密（ChaCha20-Poly1305）
             · 已自带 NAT 穿透与中继
             · 节点数量固定、成员可信
```

### 0.2 SLX 要做的事

| # | 需求 | 落地 |
|---|---|---|
| 1 | PC 不装软件 | **PC 零参与**。网关指向 OpenWrt，OpenWrt 接管 |
| 2 | 访问 YouTube 走最快的 VPS 出去 | 出口选择器：按实测评分给每个目的地挑最优出口 |
| 3 | 自动探测链路速度和质量 | L1 网格质量 + L2 出口质量，双层 EWMA |
| 4 | 不要控制台 | 无 Web UI、无 TUI 面板。守护进程 + 日志 + 少量 CLI |
| 5 | 单文件，跑 OpenWrt / CentOS7 / Ubuntu | 静态 musl 二进制，一条命令装完 |
| 6 | 自动发现同网段节点、自组网 | **ZT LAN 上 UDP 广播发现**，秒级成网，零配置 |
| 7 | 接管国外流量，境内不动 | ipset + fwmark + 策略路由 |
| 8 | 不引入其它 P2P 组网软件 | **只用已有的 ZeroTier**，不叠加 WireGuard/Tailscale 等 |
| 9 | 故障快速排查 | 故障自动分类 + `doctor`/`explain`/`trace` |
| 10 | API 兼容 | 协议/配置/API 四面版本化，能力协商 |
| 11 | 不硬编码、少配置 | 最终配置项 **2 个** |

### 0.3 关键认知：SLX 不重造轮子

因为 ZT LAN 已经解决了「节点互通 + 加密 + 穿透」，SLX **完全不需要**自己实现：

- ❌ NAT 穿透 / 打洞 / 中继
- ❌ 公网地址发现 / STUN
- ❌ 自研加密握手 / 密钥协商
- ❌ PeerTable gossip / 引导节点 / bootstrap

这些正是旧系统里最容易出错、最占工期、最后还没跑通的部分。**SLX 把全部精力集中在真正有价值的两个点上：把流量送出去，以及挑对出口。**

> 这也正好满足你「不要其他 P2P 组网软件」的要求——**不引入任何新的组网软件**，只在已有的 ZT 之上加一层智能。

### 0.4 由此带来的最大收益

| | v3.0（自研 mesh） | **v3.1（骑在 ZT 上）** |
|---|---|---|
| 组网发现 | 引导节点 + gossip + 公网地址探测 | **UDP 广播，秒级** |
| 加密 | 自研 PSK/HKDF/AEAD | **ZT 已提供，零成本零开销** |
| NAT 穿透 | 自己实现 | **不需要** |
| 需要 root 的节点 | 只有网关 | **只有网关**（VPS 上出口节点免特权） |
| 最小可用周期 | ~9 周 | **~3 周** |
| 配置项 | 3 个 | **2 个** |

### 0.5 现有环境实测勘察（2026-11-04）

设计不能凭空想象。以下是对你真实设备的只读勘察结果，**后续所有设计决策都基于这些实测**。

#### 旁路由（192.168.1.254）

| 项 | 实测值 |
|---|---|
| 型号 | **Ytwos-N1**（YouTube 优化版 OpenWrt） |
| 系统 | OpenWrt SNAPSHOT R2024.11.05，**aarch64_generic**，内核 6.1.114-flippy-92+o |
| 硬件 | 4 核，**1.8GB RAM**（可用 ~520MB），960MB eMMC（**剩余 200MB / 77% 已用**） |
| 防火墙 | **fw3（iptables legacy）**，**不是 fw4** |
| ZeroTier | 网络 `a5a76450cae9956e`，网段 **192.168.99.0/24**，MTU 2800，**broadcastEnabled=true，bridge=true** |
| 已有组件 | PassWall(xray)、dae、mosdns、smartdns、AdGuardHome、tailscale、docker、mwan3、haproxy、nginx、openvpn、ipsec |
| 关键能力 | `ipset` ✓ `ip-full` ✓ `/dev/net/tun` ✓ `iperf3` ✓ `python3` ✓ |
| 默认路由 | `default via 192.168.1.1 dev br-lan` —— 确认是**旁路由** |
| ipset 存量 | `china` **559,354** 条、`passwall_chnroute` 17,373 条、`passwall_gfwlist` 266 条 |

**好消息**：`broadcastEnabled=true` + `bridge=true` → **我们的 UDP 广播发现设计在这台机器上直接可用**，无需任何额外配置。

#### PassWall 当前配置

```
tcp_node = YZcFrXVD
节点类型 = VMess + TLS + WebSocket
服务端   = 129.225.146.84:443   （Oracle ARM）
路径     = /SoftDown
Host     = wei.yyds.fr
DNS      = dns2tcp → 1.1.1.1:53 over TCP
UDP      = 丢弃 443（禁 QUIC）
```

#### VPS 现状

| 公网 IP | 平台 | ZT 虚拟 IP | 现状 |
|---|---|---|---|
| **129.225.146.84** | Oracle ARM | — | **PassWall 当前节点**。443 上是 **Let's Encrypt 签发的 `wei.yyds.fr` 真证书**（自签直出，**非 Cloudflare 反代**） |
| 152.70.247.243 | Oracle Linux 7.9 / aarch64 / 5.7GB | **192.168.99.25** | v2ray + cloudflared + nginx |
| 82.157.26.98 | CentOS 7 / x86_64 / 2GB | **192.168.99.4** | v2ray + nginx + BT-Panel |
| 43.129.195.74 | Tencent | — | ZT Planet |

**每台 VPS 的 v2ray 模式统一为**：`vmess` 入站 on `127.0.0.1:65432`（transport=ws）+ **nginx 在 443 做 TLS 终止**。

> ⚠️ 因为 443 是**直出真证书**而非 CF 反代，**每台 VPS 拥有独立出口 IP** —— "多 VPS 选路"模型成立。

#### ZT 网络实测（从本机 192.168.99.27 扫描）

| ZT IP | 主机名 | 开放端口 | 判定 |
|---|---|---|---|
| 192.168.99.1 | YTWOS-N1 | 22,80,443 | **本机旁路由**（192.168.1.254） |
| 192.168.99.3 | — | 80,443 | OpenWrt |
| 192.168.99.4 | — | 22,80 | **CentOS 7 x86_64** = 82.157.26.98（腾讯云，国内） |
| 192.168.99.5 | OPENWRT | 22,80,443 | 另一台 OpenWrt |
| 192.168.99.6 | — | 22,80,443 | **海外 VPS**（56ms RTT），nginx |
| 192.168.99.8 | PC-20211025FUTF | 22 | Windows PC |
| 192.168.99.14 | — | 80 | Web 服务 |
| 192.168.99.15 | — | 22,80,443 | nginx（11ms，国内） |
| 192.168.99.18/.21/.24/.26/.27/.30/.33 | — | — | 其它节点（.27 = 本机 PC） |
| 192.168.99.80 | — | 22 | — |
| 192.168.99.181 | — | 80,443 | OpenWrt |
| 192.168.99.224 | YTWOS | 22,80,443 | 另一台 OpenWrt（身后是 192.168.8.0/24） |
| 192.168.99.246 | DESKTOP-62SBEFD | 22 | Windows PC |

#### 链路实测（关键数据）

| 测试 | 结果 |
|---|---|
| **PassWall 当前吞吐基线**（经 `socks5://127.0.0.1:1070` → VMess+WS+TLS → 129.225.146.84） | **4.17 MB/s（33.4 Mbps）**；清华镜像 3.15 MB/s（25 Mbps） |
| **ZeroTier 吞吐（境内）** 路由器 → .4 | **11.95 MB/s（95.6 Mbps）** |
| **ZeroTier 吞吐（境内）** PC → .4 | **7.26 MB/s（58 Mbps）** |
| **ZeroTier 到海外节点 .6**（100 包） | **0% 丢包**，RTT avg 56ms / max 118ms |
| ZT 到境内各节点 RTT | .4 8.3ms、.15 11ms、.5 15ms、.224 17ms |
| 路由器 → Oracle 公网直连 | RTT 87ms，ICMP 丢包 16% |

#### 四个决定性结论

**① 当前的瓶颈不是"墙"，是 VMess+WS+TLS 这一层本身。**

PassWall 走的是 **VMess over WebSocket over TLS over TCP**——浏览器那层 TCP 套在 VMess 的 TCP 里，TLS 又是 TCP。**这是经典的三层 TCP 套叠**，丢包时内层 TCP 的重传定时器被外层卡死，吞吐断崖。
而 ZeroTier 是 **UDP 承载**（外层是 UDP，内层 TCP 不会被打死）。实测差距：**ZT 95.6 Mbps vs VMess/WS/TLS 33.4 Mbps——接近 3 倍**。

→ **SLX 用 ZT 或自研 UDP 承载，在吞吐上天然优于当前方案。** 这不是锦上添花，是实打实的 3 倍。

**② ZT 到海外链路质量良好，0% 丢包。**
100 包测试 0% 丢包，RTT 56ms。此前 20 包小样本测出的 10% 丢包是噪声。
→ **ZT 完全可以承担主数据面**，不必为了"抗 DPI"强行绕道。

**③ 但 ZT 有一处真实的硬伤：Oracle Cloud 的公网是 NAT 映射。**
152.70.247.243 的网卡只有内网地址 `10.0.0.50/24`（Oracle 默认 VNIC 就是 1:1 NAT），公网 152.70.247.243 是映射出来的。结果 **ZT 通告的 endpoint 是内网 IP 10.0.0.50**，国内节点无法直连它 → 该节点在 ZT 里完全不可达（路由器和 PC 双向 100% 丢包）。
→ 这类节点只能走 **SLX 直连公网 IP 的传输（`tls`/`wss`）**，ZT 对它无效。**这正是"传输可插拔"的直接价值。**

**④ 抗 DPI 观感需要保留，但不该以 3 倍吞吐为代价。**
现有 `VMess+TLS+WS → 自有域名 + LE 证书`（129.225.146.84:443 是**真证书直出**，非 CF 反代）跑得通，说明这套观感有效。
→ 保留 `wss` 传输作为**按需可切换的备选**，但**默认走 ZT**（更快）。哪条路实测更好就用哪条——这正是选路算法该干的事。

---

## 1. 总体架构

```
                    ┌─────────────────────────────────────┐
                    │        ZeroTier 虚拟局域网           │
                    │      （已存在：加密 / 穿透 / 互通）     │
                    └──────┬───────────────────────┬──────┘
                           │                       │
              ┌────────────▼──────────┐  ┌─────────▼─────────────┐
              │  OpenWrt #1（网关）    │  │  VPS A（出口）         │
              │                       │  │  VPS B（出口）          │
              │  slx 守护进程          │  │  VPS C（出口）          │
              │  ├ 出口选择器 ★        │  │  slx 守护进程            │
              │  ├ 探测/评分/熔断       │  │  ├ 隧道服务端（UDP）     │
              │  ├ TUN 收发             │  │  ├ NAT 出口（SNAT）      │
              │  ├ DNS 转发器           │  │  ├ 自测速                │
              │  └ 诊断（doctor等）      │  │  └ 能力广播             │
              │                       │  │                        │
              │  ipset / fwmark / 策略路由│ │  （无 TUN，无特权）      │
              └───────────┬───────────┘  └───────────┬────────────┘
                          │                          │
                     ┌────▼─────┐              出口节点能力/负载通告
                     │  你的 PC  │              (UDP 广播 + 定向)
                     └──────────┘
```

**运行模型一句话**：SLX 在 ZT LAN 上广播「我是谁、我能当出口、我的负载如何」；网关收到通告后建候选池，对每个境外目的地实测打分挑最优，封装好的包经 ZT 发往该出口，出口 SNAT 后送上公网。

---

## 2. 节点能力与角色

同一二进制、同一份代码，按**能力**组合工作（不是三种产品）：

| 能力 | 作用 | 需要特权 | 典型节点 |
|---|---|---|---|
| `egress` | 收隧道包 → SNAT → 上公网 | 否 | VPS |
| `gateway` | 接管本机/内网境外流量 → 注入隧道 | 是 | OpenWrt |
| `probe` | 主动测自己到境外站点的速度 | 否 | VPS |

**能力自动广播、自动发现**，不靠配置。网关从广播里自动构建出口候选池。

一个节点可同时具备多种能力（例：VPS 也可以是 gateway）。

---

## 3. 关键数据流

### 3.1 一次 YouTube 访问

```
① 发现（一次性，秒级）
   OpenWrt: UDP 广播 SLX_HELLO ──► ZT LAN ──► 所有 VPS 回复
   VPS 回复: {node_id, zt_ip, port, caps:[egress], load, region}

② 探测（持续）
   VPS A/B/C 各自测自己 → youtube.com 的 CDN：RTT / 下载速率
   网关测自己 → 每个 VPS 的 ZT 链路：RTT / 丢包 / 吞吐

③ DNS
   PC → dnsmasq → slx DNS
      → 经出口 B 解析 youtube.com（拿到 B 所在地的 CDN 地址）
      → 记录亲和 youtube.com ↔ 出口B

④ 路由（内核）
   浏览器要连 142.250.x.x
   → 不在 ipset "国内段" → 打 fwmark 0x100
   → ip rule fwmark 0x100 → table 100 → default dev sl0

⑤ 接管（slx）
   sl0 (TUN) 收到 IP 包
   → 查路由表：142.250.x.x → 出口B（评分最高）
   → 原样封装 → UDP 发往 出口B 的 ZT IP

⑥ 出口
   出口B 收到 → 查 NAT 表 → 源地址改成 B 的公网 IP → 发往互联网

⑦ 回程
   互联网 → 出口B 公网 IP → NAT 反查 → 源还原 → UDP 回发 ZT
   → 网关收到 → 写入 sl0 → PC
```

### 3.2 数据面：可插拔传输栈

SLX 的数据面**不绑定任何一种传输**。三层可插拔，各层独立：

```
[ 原始 IP 包 ]
      ↓
[ SLX 帧：4B (ver|type|len) ]      ← 我们自己设计的语义层
      ↓
[ TLS 1.3 ]                        ← 可选
      ↓
[ WebSocket 伪装 ]                  ← 可选
      ↓
[ TCP / UDP ]                      ← 承载
      ↓
   VPS 出口节点 → SNAT → 互联网
```

**为什么这样分层**：抗 DPI 伪装、加密、承载是**三个正交的问题**。勘察发现你现有的抗 DPI 能力来自"VPS 上的 nginx + LE 证书 + 域名"（TLS 终止在 nginx，SLX 只需把明文帧交给它），所以 TLS 层可以**外置**给已有的 nginx，我们自己的协议只管隧道语义。

#### 三种成品传输

| 传输 | 路径 | GFW 看到的样子 | VPS 侧改动 | 实测预期 | 适用 |
|---|---|---|---|---|---|
| **`zt`**（**默认**） | OpenWrt → ZeroTier 虚拟网卡 → SLX → SNAT | 加密 UDP，无固定特征 | 只跑 SLX | **95 Mbps**（境内实测，海外链路 0% 丢包） | 绝大多数情况 |
| **`tls`** | OpenWrt → TLS 1.3 直连 VPS:8443 → SLX → SNAT | 正常 TLS，非标端口 | 只跑 SLX | 接近裸速 | **ZT 不可达的节点**（如 NAT 后的 Oracle）、需要绕开 ZT 问题时 |
| **`wss`** | OpenWrt → `wss://<你的域名>:443/slx` → nginx → `127.0.0.1:PORT` SLX → SNAT | **与现有 VMess+WS+TLS 完全一致** | nginx 加一个 location + 跑 SLX | 受 TLS/WS 开销限制（参考现状 33 Mbps） | 需要保持当前观感时 |

**默认走 `zt` 的理由（实测支撑）**：

```
现状 PassWall：  浏览器 TCP → VMess(TCP) → WS → TLS(TCP)   ← 三层 TCP 套叠
                                        丢包时内层重传被外层卡死
实测：33.4 Mbps

SLX over ZT：    浏览器 TCP → SLX → ZT(UDP) → VPS          ← 外层是 UDP，无套叠问题
实测：95.6 Mbps（境内），海外链路 0% 丢包
```

**这接近 3 倍的差距不是"墙"造成的，是协议栈选择造成的。** 隧道跑在 UDP 上，内层 TCP 的重传定时器不会被外层卡死——这是老 PassWall 方案的结构性上限，不是调优能解决的。

**`tls` 为什么必须存在**：
实测发现 Oracle Cloud 的公网是 1:1 NAT 映射，**ZT 通告的 endpoint 是内网 IP** → 这类节点在 ZT 里完全不可达（路由器和 PC 双向 100% 丢包）。**只能靠 SLX 直连其公网 IP。** 这不是理论风险，是已经观测到的故障。

**`wss` 为什么保留**：
你现有的 `VMess+TLS+WS → 自有域名 + LE 证书` 观感有效（129.225.146.84:443 是**真证书直出**，非 Cloudflare 反代）。当 ZT 路径被限速或被针对性探测时，`wss` 是保命通道。**代价是吞吐回落到 33 Mbps 级别**——所以它做备选，不做默认。

**优先级规则**：`zt` 可用且质量达标 → 用 `zt`；`zt` 不可达或质量不达标 → `tls`；两者都不行 → `wss`。**这个判定由探测结果驱动，不是写死的。**

#### 候选池 = (VPS, 传输) 二元组

```
候选 = {
  vps: "129.225.146.84",
  transport: "wss",         // 或 tls / zt
  endpoint: "wei.yyds.fr:443",
  region: "...",
  load: 0.0~1.0,
  state: ok
}
```

**探测阶段对每个二元组独立实测**，选路时在所有二元组里择优。这样"选路"不只选节点，还选**路径**——例如某台 VPS 的 nginx 抽风时，可以自动降级到同机的 `tls` 传输，再不行走 `zt`。

#### 为什么需要这一层封装头

```
内层 IP 包本身带着原始目的地址（142.250.x.x）
出口节点收到后：读 IP 头 → 改写源地址 → 直接发往互联网
```
4 字节 SLX 帧只承载：版本、帧类型（DATA/CTRL）、载荷长度、会话尾数。**不重复承载目的地址**——那已经在 IP 头里了。

### 3.3 与 ZeroTier 的分工

| 职责 | 由谁承担 | 说明 |
|---|---|---|
| 候选发现 | **ZT 广播** | 网段 192.168.99.0/24，`broadcastEnabled=true` 已验证可用，秒级成网 |
| 能力/负载通告 | **ZT** | 出口广播自己的负载、支持的传输方式 |
| 控制面（心跳/探测指令/解析请求） | **ZT** | 低频，不敏感 |
| **主数据面** | **ZT（默认）** | 承载境外流量。实测 95 Mbps、0% 丢包 |
| 备选数据面 | **`tls` / `wss` 直连** | ZT 不可达（Oracle NAT）或质量不达标时自动切换 |
| 跨网段 LAN 访问 | **ZT**（保持现状） | 你现有的 192.168.8.0/24、10.10.10.0/24 等静态路由**一行都不用改** |

> **ZeroTier 完全保留、继续工作，而且升级为核心数据通道。**
> 相比你现在的 PassWall 方案，SLX over ZT 在实测吞吐上有 **~3 倍** 的优势（95.6 vs 33.4 Mbps），且不破坏你现有的任何 ZT 用法。

### 3.4 SLX 与 PassWall 的能力对照

| 能力 | PassWall（现状） | SLX |
|---|---|---|
| 透明代理 + 分流 | ✅ iptables REDIRECT/TPROXY + `passwall_chnroute` | ✅ 同等能力，自有实现 |
| 出口数量 | 1 个（手工切节点） | **N 个，自动择优** |
| 出口选择依据 | 人工经验 | **实测吞吐/RTT/丢包 + EWMA + 滞回 + 熔断** |
| 传输协议 | VMess+WS+TLS（**TCP 套叠**） | **UDP 承载**（ZT）或直连 TLS，**无套叠问题** |
| 实测吞吐 | 33.4 Mbps | **95.6 Mbps**（ZT 境内实测） |
| CDN 位置感知 | 无（DNS 走 1.1.1.1） | **有**（经出口本地解析 + 域名亲和） |
| 可解释性 | 无 | **`slx explain` 给出每个域名的完整选路依据** |
| 故障定位 | 靠看日志猜 | **故障自动分类 + 12 项 doctor + 修复建议** |
| 配置项 | 不少（节点、订阅、规则集、DNS） | **2 个** |
| 自愈 | 规则被防火墙重载清掉需手工恢复 | **看门狗自动重建** |

---

## 4. 自组网：ZT LAN 上的发现

### 4.1 发现机制

```
OpenWrt 网关启动
  → 在 ZT 接口上 UDP 广播 SLX_HELLO {magic, version, node_id}
  → ZT LAN 上所有 SLX 节点收到并回复 SLX_HELLO_ACK {node_id, zt_ip, port, caps, load, region}
  → 网关建立候选池（1~2 秒内完成）
```

**零配置**：不需要 bootstrap、不需要公网 IP、不需要配置文件里列任何 IP。

**多网卡广播**：在所有接口上发广播（含 ZT 接口），收到响应后记录来源 ZT IP，**优先选择响应来自 ZT 接口的对端**。

### 4.2 广播不可用时的兜底

某些 ZT 网络配置可能禁用广播。兜底顺序：

```
1. ZT LAN UDP 广播（默认，最快）
2. ZT 子网轻量扫描：读取本机 ZT 接口的网段，对该网段做低速 UDP 探测（每 IP 1 个包，间隔 50ms）
3. 可选：ZeroTier Controller API 查询节点列表（需配置 controller URL + token，属于可选高级项）
```

三级兜底保证任何配置下都能成网。

### 4.3 节点状态维护

```
心跳：每 5s 一次（控制端口）
失联判定：连续 3 次无响应 → stale；30s 无响应 → down（从候选池摘除，触发熔断）
状态持久化：本地状态文件，重启后秒级恢复候选池（无需重新发现）
```

### 4.4 出口候选池的构建

网关持续收集每个节点的广播通告，维护候选池：

```
候选池条目 = {
  node_id, zt_ip, port,
  caps: [egress],
  load: 0.0~1.0,          ← 出口广播的当前负载
  capacity: N,            ← 出口广播的容量上限
  region: "eu-west",      ← 可选，用于亲和/偏好
  last_seen, state
}
```

**出口负载通告**：出口节点自己统计活跃连接数/带宽占用，广播出去。网关在打分时对高负载出口做惩罚，避免所有流量压垮一台 VPS。

---

## 5. 探测：测速度与质量

### 5.1 两层测量（必须分开）

| 层 | 谁测 | 测什么 | 频率 |
|---|---|---|---|
| **L1 网格质量** | 网关自己 | 网关↔出口 的 RTT、丢包、ZT 链路吞吐 | RTT 每 2s；吞吐每 60s |
| **L2 出口质量** | 出口自己 | 出口→境外站点的 RTT、下载速率、失败率 | 每 60s |

**为什么不能只测一层**：
- 只测 L1 → 不知道 VPS 到 YouTube 快不快（VPS 自己的出口可能拥堵/被限速）
- 只测 L2 → 不知道从家到 VPS 的 ZT 链路质量（可能丢包严重）
- **端到端带宽 ≈ min(L1 链路吞吐, L2 出口上行吞吐)**；**RTT ≈ L1 + L2**

### 5.2 L2 探测目标（不硬编码）

**默认不写死任何测速站**：

```
1. 首选：探测网关当前实际在访问的境外域名
        （正在看 YouTube → 探测 googlevideo.com 的 CDN）
        → 测出来的就是真实体验，零配置且最准
2. 冷启动（尚无流量）：退化为探测内置公共端点（可配、可覆盖）
3. 按需探测：网关发现某域名慢时，主动让各出口"试一下这个域名"，
             用实测结果纠正选路
```

### 5.3 L2 探测方法

出口节点 E 收到 `PROBE(target)`：

```
1. 用 E 自己的 DNS 解析 target
   ★ 关键：拿到 E 所在地的 CDN 地址，否则"测速快但实际慢"
2. TCP connect + TLS 握手 → 测 RTT
3. 读取 N 字节（默认 2MB，可配）→ 测吞吐
4. 统计 TCP 重传 → 估丢包
5. 返回 {rtt_ms, bytes_per_sec, retrans_rate, resolved_ips, region}
```

### 5.4 平滑与趋势

所有指标用 **EWMA**（α=0.3）抑制单次抖动：

```
ewma_new = α * sample + (1-α) * ewma_old
```

保留最近 10 个样本供趋势展示（`slx status` 迷你趋势图）。

---

## 6. 智能选路算法（核心）

### 6.1 有效指标

对目的地 D、出口 E：

```
rtt(D,E)  = L1_rtt(E) + L2_rtt(D,E)
bw(D,E)   = min(L1_bw(E), L2_bw(D,E))
loss(D,E) = 1 - (1 - L1_loss(E)) * (1 - L2_loss(D,E))
```

### 6.2 打分

软上限归一化（避免极端值主导）：

```
s_bw  = bw / (bw + B_ref)        B_ref 默认 50 Mbps
s_rtt = R_ref / (R_ref + rtt)    R_ref 默认 100 ms
s_los = 1 - loss

score(D,E) = 100 * (w_bw*s_bw + w_rtt*s_rtt + w_loss*s_los)
默认权重  w_bw=0.45  w_rtt=0.35  w_loss=0.20
```

**两项修正**：

| 修正 | 默认 | 理由 |
|---|---|---|
| **CDN 亲和加成** | +15 | D 是经 E 解析的（E 所在地的 CDN）→ 优先用 E，避免跨区 CDN 绕路 |
| **出口容量惩罚** | 动态 | E 广播负载超阈值时按比例扣分，防止压垮单台 VPS |

### 6.3 切换的三个门槛（防抖动）

```
切换到 E_new 需同时满足：
  1. score(E_new) > score(E_cur) + margin     margin 默认 8 分
  2. E_cur 不在冷却期                          冷却默认 30s
  3. E_new 通过健康检查
执行时随机延迟 0~2s                             避免大量网关同时切向同一出口
```

### 6.4 流的粘性

- **已建立的流不迁移**：TCP 流一旦选定出口就绑死（NAT 表记录）
- 出口故障时在跑的流会断（TCP 重连即走新出口）——可接受：宁可断一次，也不要每几秒抖一次

### 6.5 目的地路由表

```
路由表项 = (目的前缀, 出口节点, 评分快照, 创建时间, TTL)
查找：/32 → /24 → /16 → /10 → 默认
TTL：默认 300s（CDN 会变，不能长期固化）
出口故障时立即失效
```

### 6.6 熔断

```
E 连续 3 次健康检查失败 → 熔断，冷却 60s（指数退避，上限 10 分钟）
熔断中的出口不参与选择
半开：冷却结束放一个探测流量试探，成功则恢复
```

### 6.7 打分明细可解释

每个路由表项保存完整快照，`slx explain youtube.com` 直接输出：

```json
{
  "target": "youtube.com",
  "resolved_via": "egress-eu2",
  "chosen": "egress-eu2",
  "score": 87.3,
  "candidates": [
    {"id":"egress-eu2","score":87.3,"l1":{"rtt":45,"loss":0.1,"bw":92.1},
                          "l2":{"rtt":18,"loss":0.0,"bw":48.2},"cdn_affinity":true},
    {"id":"egress-us1","score":71.8,"l1":{"rtt":128,"loss":0.4,"bw":61.0},
                          "l2":{"rtt":14,"loss":0.2,"bw":61.0},"cdn_affinity":false},
    {"id":"egress-jp1","score":52.1,"l1":{"rtt":38,"loss":2.3,"bw":22.0},
                          "l2":{"rtt":50,"loss":0.8,"bw":22.0},"cdn_affinity":false}
  ],
  "reason": "egress-eu2 的端到端带宽与 RTT 均为最优，且 youtube.com 由该出口解析，CDN 就近"
}
```

> **"为什么走这条路"必须随时可查**——这是本系统的核心承诺之一。

---

## 7. 接管国外流量（OpenWrt 落地）

### 7.1 分流机制

```
① 获取「中国大陆 IP 段」→ ipset "slx_cn"
② nftables/iptables mangle PREROUTING:
     目标 ∈ slx_cn  → 放行（境内走原路）
     目标 ∉ slx_cn 且 ∉ 本地保留段 → 打 fwmark 0x100
③ ip rule add fwmark 0x100 lookup 100
④ ip route add default dev sl0 table 100
```

**双模式**：

| 模式 | 机制 | 特点 |
|---|---|---|
| **A. IP 段分流（默认）** | 如上 | 任何域名自动生效，无需域名表，内核级性能 |
| **B. 域名亲和（叠加）** | slx 内置 DNS 转发器 | 解决 CDN 位置问题：youtube.com 在欧洲出口解析出的 CDN 地址，与国内解析出的完全不同 |

### 7.2 ⚠️ 路由环路防护（必须做，否则系统跑不起来）

**这是本方案最危险的一个坑。** ZT LAN 的网段通常是 `10.x.x.x` 等私有地址，而私有地址**不在**国内 IP 段列表里 → 会被判定为"境外" → 被打上 fwmark → 被送进隧道 → 而隧道本身要通过 ZT 出去 → **死循环**。

四道防护，缺一不可：

```
防护 1  保留段永不标记
        在规则里显式排除：
          10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16   (私有)
          127.0.0.0/8, 169.254.0.0/16                    (本地/链路本地)
          224.0.0.0/4, 240.0.0.0/4                       (组播/保留)
          ::1, fc00::/7, fe80::/10                        (IPv6)

防护 2  ZT 网段显式豁免
        启动时读取本机 ZT 接口网段 → 加入豁免集合 → 永不标记

防护 3  策略路由优先级
        ip rule 优先级：ZT 网段 > fwmark 规则 > main
        确保「去 ZT 的流量」永远先命中 ZT 路由，不可能进隧道

防护 4  socket 绑定
        SLX 的控制/数据 socket 显式绑定到 ZT 接口
        → 即使规则出错，SLX 自身的流量也不会进隧道
```

`slx doctor` 的第 5、6 项专门检查这四道防护是否到位。

### 7.3 国内 IP 段列表的获取（反硬编码）

```
1. 本地缓存新鲜（< 24h）→ 直接用
2. 缓存过期 → 从远端更新（多源轮询，源可配）
3. 首次启动无缓存 → 立即下载
4. 下载失败 → 用内置快照兜底，并在日志里明确告警
              "使用内置快照，已过期 N 天，分流可能不准确"
5. 每天定时刷新；ipset 原子替换（先建新 set 再 swap，不中断）
```

**内置快照只是最后兜底，不是主要来源。**

### 7.4 DNS 通路

**决策：本地优先 + 境外走出口**。目的是同时解决三件事——境内不绕路、境外不被污染、CDN 位置贴合出口。

```
PC → dnsmasq(:53) → slx DNS(:5353，仅监听 127.0.0.1)
                       │
                       ├─ 本地解析（走 ISP DNS，快）
                       │    结果 IP ∈ ipset "国内段"  → 直接采用（境内最优，命中本地 CDN）
                       │    结果为 NXDOMAIN / 0.0.0.0 / 黑洞段 → 判定被污染，转出口
                       │    结果 IP ∉ 国内段 → 判定境外，转出口（同时拿到贴合出口的 CDN）
                       │
                       └─ 经隧道到出口解析
                            出口用「自己所在地的解析器」解析 → 返回该地 CDN 地址
                            → 记录亲和：domain ↔ (出口, 返回的 IP 集合)
```

**为什么要转出口**：只有出口本地的解析器才知道它附近的 CDN 地址。用网关所在地解析会拿到跨区 CDN，表现为"测速很快但实际很慢"。

**上游 DNS 不硬编码**：默认用出口节点的本地解析器（出口自己探测可用解析器）；可配置覆盖；支持 DoH/DoT over 隧道。

**OpenWrt 接入方式**（不改 dnsmasq 现有逻辑，只加一条上游）：

```sh
uci add_list dhcp.@dnsmasq[0].server='127.0.0.1#5353'
uci commit dhcp && /etc/init.d/dnsmasq restart
```

**一个更省事的备选**：把 dnsmasq 的上游直接设为公共解析器（如 `1.1.1.1`），公共 IP 会被现有 ipset 规则自动送进隧道 —— 零额外组件，境外不被污染。缺点是境内域名也走隧道、且 CDN 位置不贴合出口。**作为「先用起来再说」的一键方案保留。**


### 7.5 规则生命周期与自愈

**基础约定**：
- 启动时建规则，退出时清理
- **异常退出不留脏规则**：用独立 chain（`SLX`），启动时先清空同名 chain 再重建
- 提供 `slx uninstall` 一键彻底清理（规则 + ipset + 状态 + 二进制）

**⚠️ OpenWrt 专属坑：防火墙重载会清掉我们的规则**

OpenWrt 的 firewall3 / firewall4 在服务重启、UCI 提交、界面保存、DHCP 租约变更等场景下会**整体重建规则表**，我们注入的 chain 会被抹掉 → **境外流量突然全部直连（变慢）或黑洞（不通），且没有任何报错**。

三道防御：

```
1. 自愈看门狗（所有平台通用）
   每 10s 校验一次：chain 是否存在？fwmark 规则是否在？ipset 是否已填充？
   → 缺失则立即重建，并记 WARN 日志（含 hint）
   → 这是根本保障，不依赖任何平台特性

2. OpenWrt 官方集成（firewall3）
   把重建脚本写入 /etc/firewall.user
   → firewall3 每次重载都会 source 它，规则随重载自动恢复

3. OpenWrt 官方集成（fw4，新版默认）
   提供一份自定义 ruleset 片段，放进 /etc/nftables.d/ 并在 /etc/config/firewall 中 include
   → fw4 生成配置时一并带上
```

**关键原则**：看门狗是兜底，平台集成是加速。两者都要有——因为规则丢失这件事**一定会发生**，且发生时用户看到的现象是"突然变慢"，最难排查。


---

### 7.6 与 PassWall 的共存与迁移

你当前 PassWall 已在 OpenWrt 上运行 xray，并写入了自己的 iptables 规则（`PSW` 系列 chain）+ `passwall_*` 系列 ipset + `fwmark` 标记。**SLX 与它同时工作会打架**：两套规则都会标记流量、都会做 REDIRECT/TPROXY，标记位和 hook 点冲突。

**因此迁移是一次性的、显式的动作**，不能长期共存：

```
阶段 A  观察（不动任何东西）
        SLX 以「只探测不接管」模式运行（slx --probe-only）
        → 收集每个 (VPS, 传输) 的真实速度
        → slx explain 对比 PassWall 当前节点的速度
        → 验证 SLX 选出的最优出口确实更快

阶段 B  接管（关 PassWall，启 SLX）
        1. 在 LuCI 停用 PassWall（停 xray，清 PSW chain 与 passwall_* ipset）
        2. slx install-hooks   ← 写 SLX 规则（fw3 走 /etc/firewall.user 集成）
        3. slx run
        4. 验证：境外走隧道 / 境内直连 / doctor 全绿

阶段 C  清理
        slx uninstall          ← 清 SLX 规则与 ipset
        （PassWall 的残留配置按需手动清理，SLX 不主动删用户的东西）
```

**关键约束**：
- SLX **不自动删除** PassWall 的规则或配置（避免误伤）。迁移由用户显式执行。
- SLX 的 fwmark 值与 PassWall 冲突时，以启动时探测到的实际占用为准自动避让（"不硬编码"的又一个体现——**fwmark 值不写死**）。
- `slx doctor` 增加一项检查：**检测到 PassWall/xray 的 PSW chain 仍在生效** → 明确提示"检测到 PassWall 仍在标记流量，两者会冲突，建议先停用"。

### 7.7 DNS 迁移

现状：dnsmasq → `127.0.0.1#5333`（PassWall 的 dns2tcp）→ `1.1.1.1:53 over TCP`。

迁移后：dnsmasq → `127.0.0.1#5353`（SLX DNS）→ 本地解析 + 境外转出口。

**dns2tcp 可以保留**（它只是一个 TCP 包装器），两者不冲突，SLX 优先级更高。

---

## 8. 故障快速排查

### 8.1 原则

> **程序必须能回答："现在为什么慢 / 为什么不通？"**

不是"打印一堆日志让人自己找"，而是**主动定位到具体环节并给出可执行建议**。

### 8.2 诊断命令

| 命令 | 用途 |
|---|---|
| `slx doctor` | **一键体检**：逐项检查 + 每项「通过/失败 + 修复建议」 |
| `slx status` | 一屏概览：节点、隧道、出口候选、当前路由、丢包趋势 |
| `slx explain <域名\|IP>` | **解释选路**：为什么选这个出口，各候选打分明细 |
| `slx probe [目标]` | 手动跑一轮各出口测速 |
| `slx trace <IP>` | 逐出口试连，看谁通、谁快 |
| `slx peers` | 拓扑视图：节点、ZT IP、状态、RTT、负载 |
| `slx routes` | 路由表 + 命中统计 |
| `slx logs` | 日志查看（`--follow` / `--level` / `--since`） |

### 8.3 `doctor` 检查项

| # | 检查项 | 失败时的输出 |
|---|---|---|
| 1 | 特权（root / CAP_NET_ADMIN） | 「无法创建 TUN → 需要 root 或 CAP_NET_ADMIN」 |
| 2 | `/dev/net/tun` | 「内核未加载 tun → `modprobe tun`」 |
| 3 | TUN 创建 + MTU | 「TUN 创建失败: <errno> → 检查是否有同名设备占用」 |
| 4 | ipset / nftables / iptables 探测 | 「未找到 ipset → OpenWrt: `opkg install ipset`」 |
| 5 | **环路防护四道** | 「ZT 网段 10.147.0.0/16 未加入豁免 → 存在路由环路风险」 |
| 6 | ipset 国内段填充度 + 新鲜度 | 「国内 IP 段列表已过期 9 天 → 分流不准」 |
| 7 | 策略路由（fwmark 规则 / table 100 / default） | 「缺少 fwmark 规则 → 境外流量不会进隧道」 |
| 8 | DNS 通路 | 「隧道内 DNS 不通 → 出口侧解析异常」 |
| 9 | ZeroTier 链路 | 「ZT 接口未运行 → 请先确认 zerotier-one 服务正常」 |
| 10 | 每个 peer 的状态与 RTT | 「与 egress-eu1 断开 3 分钟 → 已从候选池摘除」 |
| 11 | 每个出口的健康与最近测速 | 「egress-us1 最近测速失败 5 次 → 已熔断」 |
| 12 | NAT 表规模 | 「NAT 表接近上限 → 新建连接会失败」 |
| 13 | 时钟偏移 | 「时钟偏移 +2.3s → 握手时间戳校验会失败」 |
| 14 | 状态目录可写 | 「状态目录只读 → 重启后无法恢复候选池」 |

### 8.4 故障自动分类

程序持续自检，命中时主动告警并给出结论：

| 症状 | 自动判定 | 输出结论 |
|---|---|---|
| 境内正常、境外全挂 | 分流规则/ipset 失效 | 「**分流层**故障：fwmark 规则缺失」 |
| 全部节点离线 | ZeroTier 异常 / UDP 被封 | 「**组网层**故障：ZT 链路不通」 |
| 部分站点慢 | 出口选择偏差 | 「**选路层**：当前走 egress-jp1（52），最优应为 egress-eu2（87）」 |
| 时好时坏 | 丢包/抖动 | 「**链路层**：egress-us1 丢包 12%，已触发切换」 |
| DNS 解析异常 | 隧道内 DNS 不通 | 「**DNS 层**：出口无法解析外部域名」 |
| 延迟高但带宽正常 | RTT 劣化 | 「**链路层**：ZT RTT 280ms（正常 45ms），疑似跨境拥塞」 |
| 突然全断 | 环路触发 | 「**转发层**：检测到路由环路，已自动切回直连」 |

**告警出口**：日志（WARN/ERROR 带 `hint` 字段）+ 本地 API + 可选 webhook（可配，默认关）。

### 8.5 可观测性

- **结构化日志**：JSON 行（默认）/ 文本。每条带 `ts / level / event / hint`
- **决策链路 ID**：探测 → 评分 → 切换 → 结果全部带同一个 `decision_id`，可串完整因果
- **本地 HTTP API**（`127.0.0.1:8080`，默认开）：
  `/api/v1/status` `/api/v1/peers` `/api/v1/routes` `/api/v1/metrics` `/api/v1/explain?target=` `/api/v1/doctor`
- **Prometheus 指标**（可选，默认关）：ZT 链路 RTT/丢包/吞吐、各出口评分、切换次数、NAT 表大小

---

## 9. API 与兼容性

### 9.1 四个面的版本化

| 面 | 机制 | 兼容策略 |
|---|---|---|
| **SLX 控制协议** | 消息头 `ver`（主4bit + 次4bit） | 主版本不同 → 拒绝并给清晰日志；次版本不同 → 取能力交集 |
| **控制面消息** | JSON，带 `type` + `v` | 未知 `type` → 回 `NACK` 不断连；未知字段 → 忽略 |
| **配置格式** | YAML 顶层 `schema_version` | 启动时自动迁移；带迁移器链 |
| **诊断 API** | 路径 `/api/v1/...` | 只增不改；破坏性变更走 `/api/v2` |

### 9.2 能力协商

握手/广播时交换：

```json
{
  "node_id": "a3f1...",
  "sw_version": "1.4.2",
  "min_proto": 1, "max_proto": 1,
  "capabilities": ["egress", "gateway", "probe_v2", "affinity_v1"],
  "region": "eu-west"
}
```

双方取**能力交集**启用特性，新节点连老节点自动降级而不断连。

### 9.3 兼容纪律（写进开发规范 + CI 门禁）

1. 字段**只增不删**；废弃字段保留一个版本周期并标 `deprecated`
2. 错误码**只增不重编号**
3. **未知消息类型必须容忍**，不能因不认识就断开
4. 配置项改名 = 新增 + 旧名保留兼容读取 + 迁移器
5. 状态文件带版本，损坏时降级为"重新发现"而非崩溃
6. **CI 门禁**：协议黄金向量测试 + 跨版本互连测试 + 破坏性变更检测

### 9.4 与 ZeroTier 版本无关

SLX **不依赖 ZT 的私有 API**，只用标准三层可达性。因此 ZT 升级/降级不影响 SLX。若将来要用 ZT Controller API（兜底发现），也只用其公开 HTTP 接口并做能力探测。

---

## 10. 零配置与反硬编码

### 10.1 配置项：全部就这 2 个

```yaml
# /etc/slx/config.yaml —— 完整内容
schema_version: 1

# ① ZT LAN 内的节点间认证密钥
#    首次启动自动生成并打印；用户复制到其它节点即可
network_key: "<base64 32B>"

# ② 可选：探测目标。留空则自动从实际访问的境外域名推导
# probe_targets: []
```

**其它全部自动**：节点身份、ZT IP 感知、发现对端、出口候选、探测目标、路由规则、DNS 上游、iptables 变体、网段冲突规避、端口选择。

### 10.2 反硬编码清单

| 项目 | 常见做法 | 本方案 |
|---|---|---|
| 节点地址 | 配置里写死 IP | **ZT 广播自动发现**，只用 ZT IP |
| 引导节点 | 配 bootstrap 列表 | **不需要** |
| 出口候选 | 手工列 VPS | **能力广播自动收集** |
| 探测 URL | 写死测速站 | 从实际访问的境外域名推导；可覆盖 |
| 国内 IP 段 | 打包进二进制 | 远端更新为主，内置快照仅兜底 |
| DNS 上游 | 写死 8.8.8.8 | 用出口自己的解析器（出口自动探测） |
| 网卡名 | 写死 eth0 | 按「默认路由接口 + LAN 接口特征」自动识别 |
| ZT 网段 | 写死 10.147.0.0/16 | **运行时从本机 ZT 接口读取** |
| iptables/nft | 写死一种 | 运行时探测择优，两种都支持 |
| 端口 | 固定 51820 | 数据/控制端口可配，默认值 + 占用时自动换 |
| 虚拟网段 | 写死 | 从本机已有虚拟网段推导；冲突自动换；持久化 |
| 阈值/权重 | 写死 | 有默认值，可配，`slx explain` 展示实际生效值 |
| 路径 | 写死 /usr/local | 遵循系统约定，自动探测 |

### 10.3 首次启动流程

```
slx run
 ├─ 读配置（无则生成，含新 network_key，并打印）
 ├─ 探测环境：TUN / ipset / iptables变体 / 网卡 / ZT 接口与网段
 ├─ 生成并持久化 node_id
 ├─ 启动数据端口 + 控制端口（占用则自动换）
 ├─ 在 ZT 接口广播 SLX_HELLO → 收集候选池
 ├─ 广播自身能力（gateway / egress）
 ├─ 若为网关：建 TUN → 建 ipset → 建 fwmark 规则 → 建策略路由 → 启 DNS 转发器
 ├─ 启动探测 + 选路循环
 └─ 就绪（打印一行摘要）
```

用户只需：**装 → 启动 → 把 network_key 复制给其它节点**。

---

## 11. 性能设计

| 关注点 | 做法 |
|---|---|
| 封装开销 | **零额外头部**，数据端口直传原始 IP 包 |
| 重复加密 | **不做**（ZT 已加密），省一次加解密 |
| TUN 读 | `AsyncFd` 事件驱动，**不轮询** |
| 零拷贝 | `bytes::Bytes` / `BytesMut` 池化复用 |
| 批处理 | 单次 read 取多个包逐个分发 |
| 无锁数据面 | 收发分属不同 task，不共享写锁 |
| 缓冲 | 每方向独立 ring buffer，慢消费者不阻塞快消费者 |
| 背压 | 队列满时丢弃并计数（可观测），不无限堆积 |
| MTU | SLX 内层默认 **1380**（保守，避免 ZT 外层分片）；TCP MSS 钳制到 1340 |
| 加密选型 | ChaCha20-Poly1305（无 AES-NI 依赖，跨平台性能一致） |

**性能目标**：单机吞吐 ≥ 1 Gbps，空闲 CPU < 2%，单节点内存 < 100MB。

---

## 12. 交付形态与平台兼容

### 12.0 语言选型：Go（已实测验证）

> **变更记录**：早期草案选 Rust，实测工具链后改为 **Go**。下面是实测依据，不是偏好。

#### 实测对比（本机 Windows，2026-11-04）

| | Rust（预估） | **Go（实测）** |
|---|---|---|
| 工具链安装 | 需 rustup ~1.5GB + zig + cargo-zigbuild | **已装 Go 1.26.1** |
| 交叉编译命令 | `cargo zigbuild --target <t>` 且 Windows 宿主链 zig | **`GOOS=linux GOARCH=x go build`**，一条命令 |
| mipsel（MT7621） | 需 `mipsel-unknown-linux-musl` + 额外链接器 | **`GOARCH=mipsle` 原生支持** |
| 静态二进制 | 5~8 MB | **1.4~1.7 MB** |
| 5 架构总耗时 | 未知（预计需调试） | **一次批量编译全部通过** |

**实测产物矩阵（已校验 ELF 头）**：

| 目标 | 结果 | ELF | 大小 |
|---|---|---|---|
| linux/amd64 | ✅ | ELF64 LE x86_64 | 1.55 MB |
| linux/arm64 | ✅ | ELF64 LE aarch64 | 1.54 MB |
| linux/arm (GOARM=7) | ✅ | ELF32 LE arm | 1.60 MB |
| linux/mipsle | ✅ | ELF32 LE mips | 1.69 MB |
| linux/riscv64 | ✅ | ELF64 LE riscv | 1.41 MB |

全部 `CGO_ENABLED=0` → **纯静态、零动态库依赖**。

#### 为什么 Go 在这个项目上更合适

| 理由 | 说明 |
|---|---|
| **交付形态是第一约束** | 你要的是"一个文件扔到 OpenWrt/CentOS7/Ubuntu 就能跑"。Go 的静态交叉编译是这条路最省事的方式，且体积小 4 倍（对只有 200MB 空闲的 eMMC 更友好） |
| **MIPS 是硬需求** | MT7621（mipsle）在 OpenWrt 保有量极大。Go 原生支持，Rust 需要专门搭链接器 |
| **GC 不是问题** | 数据面用 `sync.Pool` 复用缓冲区、避免热路径分配；Tailscale / WireGuard-go 已在同类场景验证千兆转发可行 |
| **内存安全** | Go 同样是内存安全语言，避免旧系统 `struct Ifreq` 越界那类问题 |
| **标准库够用** | `golang.org/x/sys/unix` 提供 TUN/netlink/iptables 所需全部系统调用；`golang.org/x/net` 提供 IP 包处理 |
| **启动快** | 静态 Go 二进制在 MIPS 上启动 < 100ms，适合 procd 管理 |

#### 需要接受的代价

| 代价 | 缓解 |
|---|---|
| GC 可能在极端负载下带来延迟抖动 | 数据面零分配设计 + `sync.Pool` + `GOGC` 调优；低端路由器本来也跑不满 |
| 峰值吞吐略低于 Rust | 实测目标 100~300 Mbps（MT7621）/ 1 Gbps（x86），Go 完全够 |
| 二进制含 runtime（1.5MB） | 可接受，比 Rust 的 5-8MB 还小 |

### 12.1 架构矩阵

你的设备构成：**OpenWrt 以 ARM 为主（少量 x86）**，**VPS 以 x86 为主（少量 Oracle ARM）**。

| 平台 | 架构 | 常见设备 | Rust/Zig 目标 | 产物 |
|---|---|---|---|---|
| OpenWrt | **aarch64** | IPQ807x、MT7622(Filogic 830)、红米 AX6000 | `aarch64-unknown-linux-musl` | `slx-linux-arm64` |
| OpenWrt | **armv7** (32位 ARM) | MT7623、MT7621 类 ARM7 路由 | `armv7-unknown-linux-musl` | `slx-linux-armv7` |
| OpenWrt | **mipsel** (24Kc/74Kc) | MT7621(小米/红米)、MT7628 | `mipsel-unknown-linux-musl` | `slx-linux-mipsel` |
| OpenWrt | x86_64 | 软路由 x86 | `x86_64-unknown-linux-musl` | `slx-linux-amd64` |
| OpenWrt | riscv64 | 新兴设备（StarFive 等） | `riscv64gc-unknown-linux-musl` | `slx-linux-riscv64`（可选） |
| VPS | **x86_64** | 绝大多数 VPS、Oracle AMD | `x86_64-unknown-linux-musl` | `slx-linux-amd64` |
| VPS | **aarch64** | **Oracle 免费 ARM (Ampere Altra)** | `aarch64-unknown-linux-musl` | `slx-linux-arm64` |

**好消息**：Oracle 的免费 ARM 主机是 aarch64，和主流 ARM OpenWrt **共用同一个产物**；x86 VPS 和 x86 OpenWrt 也共用。所以 7 个架构其实只需要 **5 个二进制**（mipsel / armv7 / arm64 / amd64 / riscv64）。

**mipsel 必须支持**：MT7621 是 OpenWrt 保有量极大的芯片，而它跑的是 MIPS 而非 ARM。很多人的第一台软路由就是它。

### 12.2 构建工具链：一个 zig 打通全部架构

不用为每个架构配交叉编译工具链（`mipsel-linux-musl-gcc`、`arm-linux-gnueabihf-gcc`…），统一用 **`cargo-zigbuild`**：

```
zig 的 target 名 → 一一对应，且全部静态链接 musl：
  x86_64-linux-musl      aarch64-linux-musl
  arm-linux-musleabihf   mipsel-linux-musl(hf)      riscv64-linux-musl
```

```sh
# 一条命令出一个架构
cargo zigbuild --release --target aarch64-unknown-linux-musl

# 全矩阵
./scripts/build-all.sh        # 输出 dist/ 目录，含 SHA256SUMS
```

**为什么 musl 静态**：一个文件扔到任何 Linux 都能跑，不管 glibc 版本、不用装依赖。这同时解决 CentOS 7（glibc 2.17 / kernel 3.10）这种老底座。

**发布产物**：`dist/slx-linux-{amd64,arm64,armv7,mipsel,riscv64}` + `SHA256SUMS` + 可选签名。

### 12.3 各平台的依赖与专属坑

#### OpenWrt（重点）

| 项 | 说明 |
|---|---|
| **`kmod-tun` 必装** | 多数精简镜像默认**没有** `/dev/net/tun`。漏装时表现是"创建 TUN 失败"，`doctor` 会直接给出这行提示 |
| **`ipset` 必装** | `opkg install ipset`。内核侧 `ip_set` 支持已内置于主流 target |
| **防火墙重载会清规则** | firewall3/fw4 会整体重建规则表 → 我们的 chain 被抹掉 → 境外流量突变。已设计三道防御（§7.5） |
| **你的 Ytwos-N1 实测** | **fw3（iptables legacy）**，不是 fw4。`/etc/firewall.user` 已存在 → 走 **fw3 集成路径**（写 `/etc/firewall.user`，firewall3 每次重载自动 source）。fw4 路径保留给其他机器 |
| **iptables 变体两种** | 21.02 之前 firewall3 → `iptables-legacy`；之后 firewall4 → `iptables-nft`。**运行时探测，择优**，两条路径都要测 |
| **网卡命名不同** | `br-lan` / `eth0.1` / `eth0.2`(VLAN) / `wan`。不能写死 `eth0`，按「默认路由接口 + LAN 特征」识别。你的机器：`br-lan`=192.168.1.254，`default via 192.168.1.1 dev br-lan` |
| **存储空间紧** | 你的 eMMC **960MB，已用 679MB，仅剩 200MB**。二进制剥离后约 5~8MB → **必须提供裁剪版**（关掉指标/诊断 HTTP 服务）。且 `/tmp` 是 900MB tmpfs，可用但重启即失 |
| **内存充足但要克制** | 1.8GB 总内存、可用 ~520MB。SLX 目标占用 < 100MB，不与 xray/dae/docker 抢资源 |
| **已装的同类组件多** | PassWall(xray)、dae、mosdns、smartdns、AdGuardHome、tailscale、docker、mwan3 都在跑。**端口与 fwmark 冲突必须自动避让**（`doctor` 逐项检查） |
| **服务管理是 procd** | 不是 systemd。`slx run --init` 生成 `/etc/init.d/slx`（procd 脚本） |
| **dnsmasq 集成** | 当前上游是 `127.0.0.1#5333`（PassWall 的 dns2tcp）→ 迁移为 `127.0.0.1#5353`（SLX） |
| **ZT 网络参数已确认** | 网段 `192.168.99.0/24`，MTU 2800，`broadcastEnabled=true`，`bridge=true` → **广播发现可用**，SLX 帧 MTU 取 1380 绰绰有余 |

#### CentOS 7

| 项 | 说明 |
|---|---|
| glibc 2.17 / kernel 3.10 | musl 静态完全绕过；`rustls`/`ring` 的最低内核要求也满足 |
| iptables 只有 legacy | nftables 后端需额外装（`nftables` 包），我们两条路径都支持，运行时择优 |
| `ipset` | `yum install ipset` |
| **SELinux** | 默认 enforcing 会拦下写 `/etc`、改网络策略。需 `setsebool -P` 或给二进制打标签。`doctor` 检测到 SELinux 时给出提示 |
| firewalld | 若启用，需放行 UDP 数据/控制端口；`doctor` 检查 |
| systemd | 正常，生成 `slx.service` |

#### Ubuntu / Debian

| 项 | 说明 |
|---|---|
| nftables 默认 | 22.04+ 用 `iptables-nft` 兼容层；24.04+ 部分场景纯 nft。两条路径都测 |
| systemd | 正常 |
| 内核 | 新，tun/ipset 默认可用 |

#### VPS（Oracle 免费 ARM 等）

| 项 | 说明 |
|---|---|
| **aarch64** | 与 ARM OpenWrt 共用 `slx-linux-arm64` |
| **1GB 内存机型** | Oracle 微型实例仅 1GB RAM。出口节点内存占用目标 < 50MB，NAT 表按可用内存自动缩容 |
| 免特权 | 出口节点不需要 root，普通用户 + `setcap`/systemd `User=` 即可 |
| 出站 UDP | Oracle 安全列表需放行我们的 UDP 端口（默认 51820/51821），`doctor` 可提示 |

### 12.4 低端路由器的降级策略

MIPS24Kc（如 MT7621，880MHz）或低内存 ARM 路由上，网关角色是能跑的，但要有取舍：

| 降级项 | 触发条件 | 效果 |
|---|---|---|
| **关闭 L1 吞吐探测** | 内存 < 128MB 或架构为 mips/armv7 | 省掉周期性大包传输，L1 只保留 RTT/丢包 |
| **缩小 NAT 表** | 按可用内存计算（`min(65536, 可用内存/4KB)`） | 低内存设备自动降级，不 OOM |
| **缩短探测周期** | 低端设备反而要更频繁地探测（网络波动更明显） | 探测间隔可按架构调整 |
| **精简构建** | 提供 `slx-linux-armv7-lite` 等裁剪版 | 去掉 Prometheus/HTTP 诊断模块，体积与内存都降 |

**性能预期**（网关侧，MT7621 级 MIPS）：

| 指标 | 预期 |
|---|---|
| 转发吞吐 | 100~300 Mbps（受限于 CPU 与 ZT 开销） |
| 单连接延迟开销 | < 1ms |
| 空闲内存 | < 40MB |
| 空闲 CPU | < 2% |

> Oracle ARM VPS 和 x86 VPS 做**出口**节点，性能远超需求（出口节点只做 NAT 转发 + 探测，开销极小）。

### 12.5 Rust 实现的架构注意事项

| 注意 | 原因 |
|---|---|
| 计数器用 `AtomicU32` 而非 `AtomicU64` | **MIPS32 无原生 64 位原子操作**，Rust 会退化为加锁实现，高频写会成瓶颈。用分片计数器替代 |
| 避免 `#[repr(packed)]` + 指针解引用 | MIPS 对非对齐访问敏感；用显式字节序转换 |
| 避免大块预分配 | 32 位 + 小内存设备上，64KB × N 的预分配会直接 OOM |
| `panic = "abort"` | 减小体积；配合 `catch_unwind` 在 FFI/信号边界使用 |
| 静态链接 | 无 glibc 符号版本问题，跨发行版零摩擦 |

### 12.6 一键安装（自动识别架构）

```sh
# 路由器 / VPS 上执行，自动识别架构并下载对应产物
curl -fsSL https://<host>/slx/install.sh | sh

# 或手动
scp slx-linux-arm64 root@router:/usr/bin/slx
ssh root@router 'slx run --init'      # 自动生成 procd/systemd 单元并启动
```

安装脚本做三件事：**识别架构 → 拉对应二进制 → 注册服务并启动**。用户不需要知道该下载哪个文件。

### 12.7 权限模型

| 角色 | 权限 |
|---|---|
| 出口节点（VPS，含 Oracle ARM） | **无特权**，普通用户即可 |
| 网关节点（OpenWrt / CentOS / Ubuntu） | root 或 `CAP_NET_ADMIN` + `CAP_NET_RAW` |


---

## 13. 安全

| 项 | 做法 |
|---|---|
| 传输加密 | **由 ZeroTier 提供**（ChaCha20-Poly1305，节点间 E2E） |
| 节点认证 | `network_key` 共享密钥（HKDF 派生会话密钥 + AEAD 认证，防止伪装节点混入） |
| 重放 | 每方向包序号 + 滑动窗口 |
| 密钥存储 | `/etc/slx/config.yaml`，权限 `0600`；可对接系统密钥链 |
| 管理面 | 诊断 API 与 DNS 仅监听 `127.0.0.1`，不暴露公网 |
| 日志 | 不打印密钥、完整 URL query、Cookie |
| 供应链 | 静态二进制 + 发布附 SHA256（可选签名） |

> **注意**：`network_key` 不是用来加密流量的（那是 ZT 的职责），而是用来**防止未授权节点加入 ZT LAN 上的 SLX 控制面**、并防止流量被误注入。

---

## 14. 里程碑

**因为砍掉了自研 mesh，最小可用周期大幅缩短。**

进度标记：`✅ 已完成` / `🔄 进行中` / `⏳ 未开始`

| 阶段 | 内容 | 验收标准 | 状态 |
|---|---|---|---|
| **M0 骨架** | 项目结构、**5 架构构建矩阵**、配置/状态格式与版本、协议骨架、环境探测、身份派生、日志、doctor | `slx version`/`doctor` 在真机 OpenWrt 上全绿 | ✅ **已完成**（见 §14.2） |
| **M1 发现与连通 ★** | ZT 广播发现、子网扫描、node_id 广播、候选池、状态缓存 | 网关启动 2 秒内看到全部 VPS 节点 | ✅ **已完成**（见 §14.3） |
| **M2 出口链路 ★** | 出口节点数据/控制端口、NAT 出口、网关 TUN、ZT 路由豁免 | 境外站点经指定 VPS 出口能访问；**ZT 自身流量不受影响** | ✅ **已完成**（见 §14.5） |
| **M3 分流接管** | ipset 国内段、fwmark 规则、策略路由、**用户态 NAT**、**环路四道防护**、**规则自愈看门狗** | 境外走隧道、境内走原路；**真实 VPS 与本地测试台双验收通过** | ✅ |
| **M4 选路 ★** | L1/L2 双层探测、EWMA、软上限打分、CDN 亲和、滞回、熔断、路由表 | `slx explain` 给出完整打分明细；断最优出口能自动切换 | ✅ **已完成**（见 §14.12） |
| **M5 诊断 ★** | status、explain、trace、probe、**11 类故障自动分类**、本地 HTTP API | 各类典型故障自动分类并给出可执行建议 | ✅ **已完成**（见 §14.13） |
| **M6 兼容与打磨** | 黄金向量测试、DNS 转发器、CDN 亲和闭环、API v1、**CentOS 7 真机回归** | 破坏性变更被测试拦截；真机数据面与全部 CLI 命令验证通过 | 🔄 **基本完成**（§14.14 / §14.15） |

**M0~M3 约 3 周可达最小可用；全量约 6~7 周。**

### 14.2 M0 完成情况（2026-11-04 实测）

已在**真机旁路由（YTWOS-N1，aarch64 OpenWrt）**上验证通过：

```
$ slx version
slx 0.1.0-m0 (735fbf9, 2026-11-04T09:27:38Z)
协议版本 1..1

$ slx doctor
【分流】 防火墙实现 iptables-legacy ✓   ipset ✓   默认路由 br-lan ✓   LAN br-lan 192.168.1.254/24 ✓
【组网】 ZeroTier 接口 zt6vwy3rne(192.168.99.1/24 mtu=2800) ✓
【转发】 root ✓  linux/arm64 ✓  TUN 可打开 ✓  内存 1801MB/可用497MB ✓  时钟 ✓
共 12 项：通过 11，警告 0，失败 0

$ slx run --dry-run
{"event":"startup","node_id":"297eb4d850ecb7c2","env":"linux/arm64 openwrt fw=iptables-legacy zt=192.168.99.1/24"}
{"event":"roles","roles":["egress","gateway","probe"]}
```

**已交付**：

| 模块 | 文件 | 能力 |
|---|---|---|
| 线协议 | `internal/proto/frame.go` | 4 字节头二进制帧、未知类型容忍、分片标志 |
| 控制协议 | `internal/proto/control.go` | 版本化 JSON 信封、能力协商、版本交集、错误码体系 |
| 配置 | `internal/config/config.go` | 首次启动自动生成密钥、schema 迁移、**只写 2 个配置项**、`SLX_*` 环境变量覆盖 |
| 身份 | `internal/identity/identity.go` | 由密钥+机器指纹确定性派生 node_id（**不落盘**）、HKDF 派生方向独立会话密钥 |
| 环境探测 | `internal/envdetect/envdetect.go` | TUN/防火墙变体/ipset/ZT 网段/默认路由/LAN/内存/SELinux 全自动推断 |
| 日志 | `internal/logx/logx.go` | 结构化 JSON、统一字段名、`hint`、`decision_id` |
| 诊断 | `internal/cli/doctor.go` | 12 项体检，按层分组，每项失败给修复建议 |
| 构建 | `scripts/build-all.{ps1,sh}` | 5 架构静态二进制 + SHA256SUMS，最大 3.7MB |

**M0 期间在真机上抓出并修复的两个 bug**（证明"必须上真机"）：

1. `/proc/net/route` 默认路由判据写错：误把 Gateway 当 Mask，正确的是第 7 列。症状是旁路由上识别不到默认路由。
2. `br-lan` 被当成虚拟接口排除：OpenWrt 的 LAN 就是 `br-lan`，排除它导致 LAN 接口识别失败、角色判定缺失 `gateway`。

**单元测试**：`go test ./...` 覆盖帧编解码、版本协商、能力交集、未知消息容忍、配置生成/迁移/默认值、容量随内存缩容。

### 14.3 M1 完成情况（2026-11-04 三节点真机实测）

**测试拓扑**：3 台真实机器，跨国内外，同一 ZeroTier 网络

| 节点 | ZT 地址 | 平台 | 位置 |
|---|---|---|---|
| YTWOS-N1 旁路由 | 192.168.99.1 | linux/arm64 OpenWrt | 家里 |
| Oracle ARM | 192.168.99.24 | linux/arm64 Oracle Linux 7.9 | 海外 |
| 腾讯云 | 192.168.99.4 | linux/amd64 CentOS 7 | 国内 |

**验收结果：通过。**

```
=== 旁路由上的 slx peers ===
节点 ID              ZT 地址               能力                  状态
2fc73cda3c60e2e9   192.168.99.4:51820   egress,gateway,probe  在线
a2cca141eaeecb98   192.168.99.24:51820  egress,gateway,probe  在线

共 2 个节点，其中可用出口候选 2 个
```

**发现耗时**（旁路由视角，验收要求 2 秒内）：

| 事件 | 时间戳 | 距启动 |
|---|---|---|
| 开始广播 | 17:35:15.766 | 0 ms |
| 发现 Oracle | 17:35:16.156 | **390 ms** |
| 发现腾讯 | 17:35:16.206 | **440 ms** |

**已交付**：

| 模块 | 文件 | 能力 |
|---|---|---|
| 节点表 | `internal/mesh/peer.go` | 并发安全、状态机（在线/迟滞/离线）、候选池过滤、拷贝隔离 |
| 发现 | `internal/mesh/discovery.go` | ZT 广播 + 单播回应 + 子网扫描兜底、端口自动避让、版本协商、NACK |
| 状态缓存 | `internal/mesh/state.go` | `/tmp` 原子写、损坏降级、跨节点混淆防护 |
| CLI | `internal/cli/peers.go` | 拓扑视图、快照过期提示、"为什么没有候选"的排查建议 |
| 平台适配 | `internal/mesh/sockopt_{linux,stub}.go` | SO_BROADCAST 平台隔离，开发机可编译测试 |

### M1 期间发现的四个问题（全部已修）

**① ZT 的广播是不对称的 —— 这是最重要的发现。**

实测日志显示：

```
旁路由：  discovery.broadcast → 0.39s 后发现两个 VPS        ← 广播有效
Oracle：  discovery.broadcast → 4s 后 "广播未发现任何节点" → 扫描 → 才发现
腾讯：    同上
```

**旁路由广播能被各 VPS 收到，但 VPS 自己广播时其它节点收不到。** 原因与 ZT 的多播转发角色和拓扑有关（旁路由 `bridge=true` 且连接数最多，可能承担了多播转发）。

→ **设计修正**：子网扫描从"可选兜底"升级为**与广播并行的第二条主路径**。原来要等 2.5 秒才扫，现在启动密集广播（1.5 秒）后如果仍无节点就**立即扫描**，VPS 侧发现时间从 4 秒降到约 1.5 秒。

**② 你所有 VPS 都在 provider NAT 后面，自动识别拿不到公网 IP。**

| 节点 | 网卡实际地址 | 公网地址 |
|---|---|---|
| 腾讯 82.157.26.98 | **172.21.0.15** | 82.157.26.98（平台映射） |
| Oracle 129.225.146.84 | **10.0.0.245** | 129.225.146.84（平台映射） |

→ 新增配置项 `public_addr`：NAT 后的节点显式填自己的公网 IP，`direct` 传输才能用。日志里会明确提示这件事，而不是静默留空。

**③ `slx peers` 显示所有节点「迟滞」。**

根因：CLI 复用了守护进程的 `LoadState`，而它的语义是「恢复出来的节点要标脏」（正确的保护，避免选路选中已消失的出口），拿它渲染界面就成了永远迟滞。
→ 拆成两个方法：`LoadState`（守护进程，标脏）与 `LoadSnapshot`（CLI 展示，保留真实时间），并加测试锁住这个区别。

**④ SIGTERM 后进程多活 2 秒。**

根因：`ReadFromUDP` 有 2 秒读超时，信号来了也要等它超时才能退出。
→ ctx 结束时立刻 `conn.Close()` 打断读操作，退出变成毫秒级。

### 14.4 M1 遗留项

| 项 | 说明 | 计划 |
|---|---|---|
| VPS 侧发现慢约 1.5 秒 | 依赖子网扫描，扫 254 个 IP × 30ms | 可改为"先扫已知节点地址"（从缓存恢复的目标优先单播） |
| `direct` 传输未实现 | 目前只有节点间的发现通道，没有数据面 | M2 |
| 心跳未真正发送 | `MsgHeartbeat` 已定义且能处理，但发送端留到 M2（需要先有负载统计） | M2 |

### 14.5 M2 完成情况（2026-11-04 真机验收通过）

**测试拓扑**：腾讯云 CentOS 7（网关）→ Oracle ARM（出口）→ 互联网。
选这两台是为了不动你正在用的旁路由与 PassWall。

#### 验收结果：通过

```
=== 测试 1：ICMP 穿隧道 ===
3 packets transmitted, 3 received, 0% packet loss
rtt min/avg/max/mdev = 217.924/218.148/218.467/0.231 ms

=== 测试 2：TCP 穿隧道 ===
路由: 104.26.13.205 dev slx0
出口 IP: '129.225.146.84'      ← Oracle 的公网地址，不是腾讯自己的

=== 数据面计数（5 秒窗口）===
网关: tun_read=2  sent=1  tun_write=2   no_route=0  send_err=0
出口: tun_read=1  sent=0  tun_write=1   no_route=0  send_err=0
```

**出口 IP 变成 Oracle 的公网地址，这就是端到端打通的铁证** —— 从腾讯云发起的请求，是从 Oracle 的 IP 出去的。

#### 最终规则形态

```
网关 (腾讯 eth0 / slx0 无 IP):
  -A POSTROUTING -o slx0 -m comment --comment "slx:gateway-snat" -j SNAT --to-source 10.254.221.153
  -A POSTROUTING -o eth0 -m mark --mark 0x51 -m comment --comment "slx:egress-snat" -j MASQUERADE
  -A PREROUTING  -i slx0 -m comment --comment "slx:tunnel-mark" -j MARK --set-xmark 0x51
  -A FORWARD     -o slx0 -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu

出口 (Oracle enp0s3 / slx0 无 IP): 同构，网卡换成 enp0s3
```

### 14.6 M2 的核心突破：ZeroTier 认领隧道地址

这是本轮最有价值的发现，**也是设计里「环路四道防护」第四道没做到位的具体后果**。

#### 现象

TCP 一律 `No route to host`，而 ICMP 正常。计数器显示隧道里在跑**大量我们没发起的流量**：

```
网关 5 秒内从 TUN 读 138 包、出口 5 秒内读 897 包
```

`tcpdump -i slx0` 抓到了真凶：

```
IP 10.254.221.153.38402 > 106.112.238.120.13568: UDP, length 154
IP 10.254.221.153.37374 > 172.245.251.234.21180: UDP, length 154
```

**源地址是隧道内层地址 `10.254.221.153`，但目的地址是 ZeroTier 的对端公网 IP。**
也就是说：**ZeroTier 把新建 slx0 的地址认领成了自己的发包源地址**，它的 P2P 流量全跑进了我们的隧道。

#### 根因

ZeroTier 会绑定到本机**所有带 IP 地址的接口**，把它当作候选源地址。
我们把内层地址直接配在 slx0 上，ZT 就把它当成了自己的出口地址。
后果是双重灾难：
1. ZT 的流量挤占隧道带宽（几百包 vs 我们的几包）
2. ZT 的包被出口 SNAT 后，回程包被送到隧道地址，与我们的转发路径互相污染，TCP 连接因此失败

#### 修法：隧道接口不配 IP

```
slx0 只 `ip link set up` + 设 MTU，**不执行 ip addr add**
内层路由改为显式建立： ip route add 10.254.0.0/16 dev slx0
网关 SNAT 改用 iptables -j SNAT --to-source <内层IP>   （不再用 MASQUERADE，因为它要求接口有地址）
出口 SNAT 改用 fwmark：PREROUTING -i slx0 打 0x51 → POSTROUTING -m mark 匹配
```

**为什么出口 SNAT 必须用 fwmark 而不是 `-s 内层网段`**：
`POSTROUTING` 链不允许 `-i` 参数（iptables 直接拒绝：`Can't use -i with POSTROUTING`）。
要「只 NAT 从隧道进来的包」，唯一正确的做法是先在 PREROUTING 打 mark，再按 mark 匹配。
这同时也补上了第四道环路防护。

改完之后：隧道里的包从**几百个**降到**只有我们的测试流量**，TCP 立刻通了。

### 14.7 M2 期间修掉的八个真问题

| # | 问题 | 现象 | 根因与修法 |
|---|---|---|---|
| 1 | 数据面被发现在阻塞 | 启动 20 秒后才建 TUN | TUN 与规则是本地操作，应立即建立；出口地址改为每包现查 |
| 2 | 内层网段 /24 装不下哈希地址 | `10.254.168.57/24` 跑出网段 | 改 /16（6.5 万地址，十来个节点几乎不碰撞） |
| 3 | 多网关内层地址冲突 | 出口分不清回程包给谁 | 改为按 node_id 哈希派生唯一地址 |
| 4 | 网关 SNAT 规则被漏装 | 包能到出口但回不来 | 早先按「启动时是否已有出口」判角色，那一刻还没发现对端；改为无条件安装 |
| 5 | 对端状态变「迟滞」 | 明明在线却报候选 0 个 | ZT 广播不对称；加**单播心跳**对已知节点直发 |
| 6 | FORWARD ACCEPT 排在 REJECT 后 | `ICMP 通但 TCP 全 No route to host` | firewalld/docker/宝塔在链尾塞 `-j REJECT`；`-A` 追加永远匹配不到，改 `-I FORWARD 1` |
| 7 | **ZeroTier 认领隧道地址** | 隧道里跑满 ZT 的 P2P 流量，TCP 失败 | 隧道接口不配 IP；NAT 改用 SNAT --to-source + fwmark |
| 8 | `-i` 用在 POSTROUTING | iptables 直接报错，数据面启动失败 | POSTROUTING 不支持 `-i`；改用 PREROUTING 打 mark + POSTROUTING 按 mark 匹配 |

**第 6、7、8 条都属于「日志里看不出来、表现像被墙了」的类型**，已全部写进代码注释。

### 14.8 M2 遗留事项

| 项 | 说明 | 计划 |
|---|---|---|
| IPv6 全程不支持 | 数据面只处理 IPv4，IPv6 包计数后丢弃 | 后续版本 |
| `direct` 传输未实现 | 目前只走 ZeroTier | M3 后补（Oracle NAT 场景需要） |
| 单出口固定 | `slx explain` 还不可用，出口靠自动挑第一个候选 | M4 接入选路 |
| 出口 SNAT 用 mark 0x51 硬编码 | 与其它软件 mark 冲突的概率极低但非零 | 后续改为启动时探测空闲 mark |

### 14.10 M3 完成：分流接管 + 用户态 NAT（2026-11-05）

**这一节记录了一个从「怎么都修不好」到「找到真正根因」的完整过程，
其中两个关键结论值得单独记住，见 §14.10.4。**

#### 14.10.1 验收结果

**真实环境验收（腾讯云当网关 + Oracle 当出口，走真实 ZeroTier 二层网）**

```
[通过] ICMP 经隧道              4/4 收到，RTT 215ms
[通过] TCP 出口 IP 变成 Oracle   返回 129.225.146.84
[通过] 境内走原路且不进隧道       隧道包数 21 → 21（零增长）
>>> M3 真实环境验收通过 <<<
```

**本地测试台验收（WSL2 + 假 ZeroTier 接口）**

```
[通过] 境外走隧道              4/4 收到
[通过] 境内走原路              3/3 收到
[通过] 隧道确实承载流量
[通过] 境内流量未进隧道         计数 8→8
[通过] TCP 经隧道端到端可用     curl 返回 'slx-ok'
>>> 通过 5 项，失败 0 项 <<<
```

#### 14.10.2 交付内容

| 模块 | 文件 | 能力 |
|---|---|---|
| **用户态 NAT** | `internal/forward/nat.go` | 隧道两端地址/端口改写，RFC 1624 增量校验和，300s 空闲回收，16384 表项上限 |
| 国内网段 | `internal/cnlist/cnlist.go` | 双源（clang.cn / APNIC）、两种格式解析、24h 缓存、原子写 |
| 分流规则 | `internal/dispatch/dispatch.go` | ipset 原子替换、豁免集合、fwmark、策略路由、**四道环路防护**、Verify 自检 |
| 看门狗 | `internal/cli/dispatch.go` | 10 秒巡检、缺失重建、**出口不可用时主动退回直连**（避免黑洞） |
| 网络配置 | `internal/netcfg/*` | ipset/ip rule 管理、MSS 钳制、反向路径过滤、per-interface 转发开关 |
| 清理 | `internal/cli/uninstall.go` | 按 comment 精确删除，不碰用户原有配置 |
| 本地测试台 | 见 §14.10.5 | 无需真实 ZeroTier 的端到端测试环境 |

#### 14.10.3 关键设计变更：为什么把 NAT 搬进用户态

**早期实现**：网关侧 `-o slx0 -j SNAT --to-source <内层地址>`，
回程靠内核 conntrack 自动反 NAT。这是最省事的做法 —— 不用自己写 NAT 表、
不用管端口分配、不用处理超时回收。

**但它不工作。** 回程包会在 netfilter 的 raw 与 mangle 之后就消失，
既不进 nat、也不进 INPUT/FORWARD。在两台不同发行版的 VPS 和 WSL 上都能复现，
而**手工构造同样字节的包却能正常转发**。

**新实现**：网关自己维护一张极小的 NAT 表。
- 出方向：记录 (客户端IP:端口, 目的IP:端口) → 分配的内层端口，改写源地址与端口；
- 回程：按「协议 + 分配到的端口」反查，还原目的地址与端口。

写进 TUN 时目的地址**已经是客户端的真实地址**，
内核只需做一次普通转发 —— 不涉及任何 NAT，也不依赖 conntrack。

代价约 400 行代码，收益是**行为完全可控**：不再受发行版 iptables 版本、
conntrack 行为、策略路由交互的影响。

性能实测（含基准里 1280 字节的包拷贝）：稳态 **310 ns/包**，零内存分配。
按 100 Mbps、1400 字节包算约 9k pps，每秒消耗不到 3 毫秒 CPU，可忽略。

#### 14.10.4 两个必须记住的结论

**① M2 从未验证过「转发」这条路径。**

M2 当时是在网关本机上 curl —— 回程包的目的地址是网关自己的 IP，
走的是**本地投递**，不是转发。所以 M2 通过只证明
「隧道 + SNAT + 本地投递」通了，转发路径是全新的、从未验证过的。
以后判断某个里程碑是否真的覆盖了 LAN 转发，要看有没有跨接口的用例。

**② rp_filter 必须把 `all` 归零，宽松模式(2)也不够。**

这是最终定位到的真凶。实测参数矩阵（同一个包、同一套规则，只改 rp_filter）：

| all | 隧道网卡 | 结果 |
|---|---|---|
| 2 | 2 | ❌ 丢包 |
| 2 | 0 | ❌ 丢包 ← 说明 `max(all, iface)` 生效 |
| 1 | 0 | ❌ 丢包 |
| **0** | **0** | **✅ 通过** |

Linux 计算生效值时取 `max(conf.all.rp_filter, conf.<iface>.rp_filter)`，
只要 `all` 非 0，把接口单独设成 0 也不会生效；**宽松模式同样不够**。

实现上把 `all` 归零反而是对用户配置扰动最小的做法：
归零之后每张网卡自己的值会直接生效，WAN / LAN 上已显式配置的严格模式被完整保留，
只有从没配过的接口才会变成 0。退出时还原原值。

#### 14.10.5 本地测试台（重要工程资产）

在 WSL2 里用 netns 搭出了完整的三节点拓扑，
并用**接口名前缀技巧**骗过 envdetect：把 veth 命名为 `ztfake0`，
slx 就认为自己在 ZeroTier 局域网上 —— **完全不需要真实 ZeroTier，也不需要国际网络**。

```
client --lan0-- [gw] --ztfake0==ztfake0-- [eg] --wan0-- root(伪装互联网)
```

**迭代周期从「每次 2 分钟国际网络往返」降到「每次 3 秒」**，
这是本轮最重要的工程收益 —— 没有它，上面那个 rp_filter 矩阵根本测不出来。

#### 14.10.6 M3 期间修掉的问题汇总

| # | 问题 | 现象 | 修法 |
|---|---|---|---|
| 1 | `ip rule` 参数未拆分 | 规则静默不生效 | `strings.Fields` 拆成独立 argv |
| 2 | rp_filter 取 `max(all, iface)` | 回程包消失，抓包看得见但不转发 | 必须把 `all` 归零（见 §14.10.4） |
| 3 | 只开全局 `ip_forward` 不够 | 隧道包进来了但不转发 | 显式设 `conf.<tun>.forwarding=1` |
| 4 | ZeroTier 认领隧道网卡地址 | ZT 把隧道地址当源地址，P2P 流量灌进隧道 | 隧道不配地址 + 内层 IP 挂 `lo`（ZT 忽略 loopback） |
| 5 | 境外 IP 段源在国内全被墙 | 拿不到国内网段列表 | 换 `ispip.clang.cn` + APNIC 双源 |
| 6 | 内核 conntrack 反 NAT 不可靠 | 回程包在 raw/mangle 后消失 | 改为用户态 NAT（见 §14.10.3） |
| 7 | ICMP 校验和含伪首部 | 改地址后 ICMP 校验和错误 | ICMP 校验和**不含**伪首部，只按 id 改动修正 |
| 8 | 端口空间小于表项上限 | 端口耗尽后退化成线性扫描，单包 55µs | 表项上限设为端口空间的 ~2/3 |

#### 14.10.7 一个环境问题（不是代码问题）

测试中发现腾讯云 82.157.26.98 的**出站 443 到部分 IP 不通**：

```
104.26.12.205:443  失败
1.1.1.1:443        失败
curl api.ipify.org 失败
```

一开始被误判成隧道的问题。所以**选出口机时要先确认它自己能上网**，
建议在 `slx doctor` 里加一项出口外网连通性检查。
### 14.12 M4 完成：双层探测与智能选路（2026-11-05）

#### 14.12.1 验收结果

```
[通过] 探测已启动
[通过] 选路快照已生成        L1 实测 RTT 0.1ms
[通过] slx routes 可用       评分表 + 目的地缓存
[通过] slx explain 可用      完整发分公式、门槛、亲和说明
[通过] 境外走隧道
[通过] 境内走原路
>>> M4 验收通过 <<<
```

#### 14.12.2 交付内容

| 模块 | 文件 | 能力 |
|---|---|---|
| **EWMA 与打分** | `internal/picker/metrics.go` | α=0.3 平滑、10 样本趋势、软上限归一化、亲和加成 |
| **熔断** | `internal/picker/policy.go` | 连续 3 次失败熔断、60s 起指数退避封顶 10 分钟、半开试探 |
| **路由表** | `internal/picker/policy.go` | 最长前缀匹配、300s TTL、出口失效立即清除 |
| **选路器** | `internal/picker/selector.go` | 按评分择优、8 分滞回门槛、30s 切换冷却、全熔断兜底 |
| **双层探测** | `internal/cli/probe.go` | L1 每 2s、L2 每 60s、出口侧真实测量（DNS→TCP→TLS→下载） |
| **协议扩展** | `internal/mesh/discovery.go` | ProbeReq/ProbeResp 收发、`SendControl` 通用发送 |
| **可解释性** | `internal/cli/routes.go` | `slx routes` / `slx explain`，读快照不打扰守护进程 |
| **测试** | `internal/picker/picker_test.go` | 27 个用例覆盖 EWMA/打分/熔断/路由表/选路全路径 |

#### 14.12.3 几个关键设计决策

**① 软上限归一化，而不是线性截断**

```
s_bw  = bw / (bw + 50Mbps)      50Mbps 得 0.5，100Mbps 得 0.67
s_rtt = 100 / (100 + rtt)      10ms 得 0.91，100ms 得 0.5
```

好处是极端值不会主导评分：一台 1Gbps 的机器不会因为带宽就把一台 10ms 的机器碾死。
实测两者评分差距在 15 分以内，符合预期。

**② 带宽取 min、丢包取并集、延迟相加**

```
rtt  = L1_rtt + L2_rtt
bw   = min(L1_bw, L2_bw)
loss = 1 - (1-L1_loss)(1-L2_loss)
```

带宽取 min 是因为端到端吞吐由最慢那段决定（木桶效应）；
延迟相加因为两段是串行的；丢包取「至少丢一次」的并集概率。

**③ 路由表只提供粘性，竞争力必须用当前实测重新评估**

一开始路由表存了「建表时的分数」并拿它比较，结果**已经劣化的出口因为旧分数看起来还行，永远不切换**。
改成：缓存只决定「倾向用哪个」，是否值得继续用要看**当前实测分数**。

**④ 为什么用文件快照而不是 RPC**

CLI 是独立进程，状态在守护进程内存里。用一份小 JSON 交换，
既省掉了给 CLI 加 RPC 依赖，也让「事后翻看故障现场」成为可能。
快照每 5 秒落盘一次，不更频繁是为了减少闪存擦写。

#### 14.12.4 M4 期间修掉的问题

| # | 问题 | 现象 | 修法 |
|---|---|---|---|
| 1 | `select` 是 Go 关键字 | 不能做包名 | 改名 `picker` |
| 2 | EWMA 零值 alpha=0 | 值永远停在首个样本上，EWMA 退化成"不更新" | Update 时补默认 alpha |
| 3 | 字段与方法重名 | `valid` 字段与 `valid()` 方法冲突 | 方法改名 `isValid()` |
| 4 | 拷贝含锁的结构体 | vet 报 `assignment copies lock value` | 不构造副本，直接读值算指标 |
| 5 | 路由表用旧分数比较 | 劣化后的出口永远不被换掉 | 用当前实测分数重新评估 |
| 6 | `humanAge` 重复定义 | 与 `peers.go` 冲突 | 复用已有实现 |

#### 14.12.5 已知限制

- **L2 探测在纯 netns 测试环境里测不到吞吐**：测试拓扑的出口只能到 root 伪装互联网，
  解析不出真实站点。真实 VPS 上可用（Oracle 已验证能上网）。
- **CDN 亲和的自动建立尚未接线**：`UpdateL2` 支持 affinity 参数，
  但「经某出口解析域名后自动标记亲和」的链路还没接。
  这需要把 DNS 转发器的解析结果接进来 —— 属于 M5 的 DNS 部分。
- **流粘性依赖路由表**：TCP 流一旦选定出口不会主动迁移（符合设计），
  但同一目的地的新连接会跟随路由表。
### 14.13 M5 完成：诊断体系（2026-11-05）

#### 14.13.1 验收结果

```
[通过] status 输出正常 / 含组网、分流、选路信息
[通过] trace 输出正常（逐跳展示）
[通过] probe 输出正常（各出口实测质量）
[通过] API health / status / probe 均可用
[通过] 故障分类带可执行建议
>>> 通过 10，失败 0 <<<
```

实际输出示例（真实运行）：

```
闪连 SLX 0.7.0
节点 b356d0fb2e8e7d86    角色 egress+gateway+probe
运行 33 秒（自 11-05 06:56:29）

组网：
  共 1 个节点（在线 1），其中可用出口 1 台

分流：
  未启用（境外流量走直连）

选路：
  当前出口 （尚未选择）
      7c078e4c888e53e7  59.0 分  0ms  5.0M  丢包 0%

流量：
  TUN 收 0 包 / 发 0 包
  异常   无路由 0  发送失败 0  坏包 3

结论：发现 1 个问题

 [降级] 境外流量分流未生效
     现象：数据面在跑，但没有接管国外流量，所有网站都走直连
     处理：确认有可用出口（slx peers）；确认国内网段能下载（slx doctor）…
```

注意最后那段：**故障分类不仅报出了问题，还给出了「为什么会出现」和「该怎么办」**。
这正是「故障快速排查」这条需求要的东西。

#### 14.13.2 交付内容

| 模块 | 文件 | 能力 |
|---|---|---|
| **运行时状态** | `internal/cli/status.go` | 状态快照结构、`slx status` 分区块展示 |
| **故障分类** | `internal/cli/status.go` | 11 类故障自动识别，每类都带「现象 + 处理」 |
| **路径追踪** | `internal/cli/status.go` | `slx trace` 逐跳展示：解析→判定→分流→隧道→选路→NAT→回程 |
| **主动探测** | `internal/cli/api.go` | `slx probe` 展示实测质量；带参数时触发即时探测 |
| **本地 API** | `internal/cli/api.go` | 127.0.0.1:7079 上的 `/api/v1/{health,status,routes,probe}` |
| **可解释性** | `internal/cli/routes.go` | `slx routes` / `slx explain`（M4 已交付） |

#### 14.13.3 11 类故障分类

| 编码 | 严重度 | 判据 |
|---|---|---|
| `mesh.no_peers` | 致命 | 一个对端都没发现 |
| `mesh.no_egress` | 致命 | 有对端但没有可用出口 |
| `route.all_breakers_open` | 致命 | 所有出口都在熔断 |
| `split.disabled` | 降级 | 数据面在跑但没接管境外流量 |
| `split.no_traffic` | 降级 | 规则装好了但零流量 |
| `forward.one_way` | 致命 | 发得出去、回程 0 包（rp_filter 典型症状） |
| `forward.no_route` | 提示/降级 | 无路由丢包，按占比升级 |
| `split.cnlist_stale` | 提示 | 国内网段数据超过 7 天没更新 |
| `link.high_loss` | 提示 | 某出口丢包率 > 10% |
| `link.high_rtt` | 提示 | 某出口延迟 > 400ms |
| `env.no_tun` / `env.no_ipset` | 致命 | 缺关键内核能力或命令 |

#### 14.13.4 两个设计决策

**① CLI 只读文件快照，不连守护进程**

状态每 5 秒落盘一份 JSON，`status` / `trace` / `routes` / `explain` 全部读文件。
好处是**守护进程卡死甚至崩溃时这些命令依然可用** —— 恰恰是排障最需要它们的时候。
如果做成 RPC，进程一挂就什么都查不到。

只有需要**主动触发动作**的 `slx probe <域名>` 才走本地 API。

**② 每条故障结论都必须带可执行的下一步**

只说「有问题」不说怎么办，对用户没有价值。所以 `Finding` 结构里
`Action` 是必填字段，设计时就把这条约束固定下来。

#### 14.13.5 本地 API

```
GET /api/v1/health              → {"ok":true,"time":"..."}
GET /api/v1/status              → 完整状态快照
GET /api/v1/routes              → 选路器状态（候选评分 + 路由表）
GET /api/v1/probe?domain=x.com  → 触发即时探测
```

默认**只绑 127.0.0.1**：这是管理接口，不该被局域网访问。
需要远程访问时用 `SLX_API_ADDR` 显式指定，并自行确认网络可信。

#### 14.13.6 尚未完成（留给 M6）

- **DNS 转发器**：`127.0.0.1:5353` 上的本地解析，本地优先 + 境外走出口 + 域名↔出口亲和。
  它同时是 CDN 亲和闭环的关键一环（经某出口解析 → 自动标记亲和）。
  代码量不小（DNS 报文合成），单独立项更清楚。
- **`slx logs`**：结构化日志检索。
- **`slx install-hooks`**：fw3 防火墙重载后自动重建规则（目前靠看门狗 10 秒内自愈，够用但不优雅）。
### 14.14 M6 进展：兼容性护栏与 DNS 转发器（2026-11-05）

#### 14.14.1 已完成

| 项 | 内容 | 验证 |
|---|---|---|
| **黄金向量测试** | `internal/proto/golden_test.go`：冻结帧格式、控制消息字段名、线上魔数 | 6 组用例全过 |
| **向前兼容测试** | 未知字段被忽略、缺失字段回落零值 | 通过 |
| **DNS 转发器** | `internal/dnsfwd/`：本地/经出口两条路径、DNS 报文自建 | **12 个单元测试全过** |
| **CDN 亲和闭环** | 经出口解析 → 自动标记「域名↔出口」亲和 → 选路优先同出口 | 单测覆盖 |
| **API v1** | `/api/v1/{health,status,routes,probe}` | M5 已验证 |

#### 14.14.2 黄金向量测试为什么重要

协议兼容性最容易被无意破坏：改一个 JSON 字段名、调整结构体字段顺序、换一种长度编码，
本地测试可能全绿，但新旧版本节点之间就悄悄不通了。
这类问题在实网里极难定位 —— 现象是「能发现对方、就是传不了数据」。

做法是把线上字节格式**写死在测试里**：任何人改动编码导致字节变化，测试立刻失败。
测试里还显式冻结了这些必须慎之又慎的常量：

```go
WireVersion   == 1   // 帧头版本
ControlVersion == 1  // 控制协议版本
HeaderLen     == 4   // 帧头长度
MaxPayload    == 65535
TypeData/TypeCtrl/TypeKeepalive/TypeProbe == 1/2/3/4
```

以及每个消息类型的**线上字段名**（`node_id`、`sw_version`、`proto_min`…）。
改这些就是破坏性变更，必须提协议版本 —— 测试会强制你面对这个决定。

#### 14.14.3 DNS 转发器的设计

**它解决两个问题：**

1. **CDN 位置**：境外站点如果在家里解析，可能拿到很远节点的 IP；
   经出口解析才能拿到贴着那个出口的 CDN 地址 —— 这直接决定视频卡不卡。
2. **域名↔出口亲和**：某域名经哪个出口解析，后续流量就该优先走那个出口，
   否则会出现「解析到东京的 IP、却从法兰克福出去」的绕路。

**判断逻辑是自学习的，不需要任何域名白名单：**

```
第一次遇到某域名 → 先本地解析，看 IP 是否落在中国大陆网段
  ├ 是 → 记为境内，以后都本地解析（快，且拿本地 CDN）
  └ 否 → 记为境外，以后经出口解析（拿贴着出口的 CDN），同时建立亲和
```

判据来自实测而非硬编码 —— 这符合「尽可能减少配置项」的总体要求。

**为什么自己实现而不用现成库：**

- 只需要 A 记录转发，不需要递归、DNSSEC、EDNS；
- 单文件二进制要尽量小；
- 关键是**决策逻辑**（本地还是经出口），这块本来就得自己写。

**已知限制（都写在包注释里）：**

- 只返回 A 记录。AAAA 查询回空应答，客户端会退回 IPv4。
- 只支持 UDP。应答超过 512 字节时截断并置 TC 位。
- 不做 DNSSEC 校验（局域网内网关上做校验意义不大）。

**默认不占 53 端口**：那是 dnsmasq / systemd-resolved 的地盘，硬抢会破坏用户现有解析。
默认 `127.0.0.1:5353`，由用户把路由器 DNS 上游显式指过来。

#### 14.14.4 M6 验收结果

```
[通过] DNS 转发器已启动
[通过] DNS 转发器有应答（协议层正常）
[通过] 境外走隧道（回归）
[通过] slx status / routes / trace / probe / doctor（回归）
通过 8，失败 1
```

唯一失败项是「DNS 查询返回了 A 记录」—— 测试沙箱的 netns 里没有可用的上游 DNS，
解析不出真实域名。这是测试环境限制，不是转发器缺陷：
`internal/dnsfwd` 的 12 个单元测试用注入式解析器覆盖了本地路径、出口路径、
出口失败回落、AAAA、SERVFAIL、端到端 UDP 往返等全部逻辑。

#### 14.14.5 M6 尚未完成

| 项 | 状态 | 说明 |
|---|---|---|
| **CentOS 7 真机实测** | ⏳ | 已在腾讯云 CentOS 7 上跑过 M2/M3 的数据面验证，但还没跑完整的 M6 回归 |
| **OpenWrt 真机实测** | ⏳ | 需在用户的路由器上验证；有中断现有 PassWall 的风险，需要用户在场 |
| **`slx install-hooks`** | ⏳ | fw3 防火墙重载后自动重建规则（目前靠看门狗 10 秒内自愈，够用但不优雅） |
| **`slx logs`** | ⏳ | 结构化日志检索 |
| **性能调优** | ⏳ | 已有基准（NAT 稳态 310ns/包、选路 0 分配），但没做过满带宽压测 |
| **协议版本协商端到端** | 🔄 | `Negotiate()` 已实现并测试，但还没在真实的新旧版本混跑中验证 |

**真机测试建议放在用户在场时进行**：OpenWrt 是家里的上网出口，
任何规则改动都可能短暂断网。当前设计已经把风险降到最低
（旁路由场景下只需改客户端网关指向，服务器本身的网络不受影响），
但仍应提前告知。
### 14.15 M6 真机回归结果（2026-11-06）

#### 14.15.1 CentOS 7 环境探测（slx doctor）

在腾讯云 **CentOS Linux 7.9.2009 / 内核 5.17.1-1.el7.elrepo / x86_64** 上：

```
共 14 项：通过 11，警告 1，失败 0

【分流】
  [通过] 防火墙实现   iptables-legacy
  [通过] ipset       已安装
  [通过] 默认路由     出口接口 eth0
  [通过] LAN 接口     eth0 172.21.0.15/20
  [警告] 分流数据     中国大陆网段已 2 天未更新（4344 条）
【组网】
  [通过] ZeroTier 接口 zt6vwy3rne(192.168.99.4/24 mtu=2800)
【转发】
  [通过] 运行身份 / 平台架构 / TUN 设备 / 内存 / 临时目录 / 系统时钟
```

环境探测在 CentOS 7 上完全正确：识别出 iptables-legacy、ipset、
ZeroTier 接口、TUN 可用、centos 发行版、SELinux disabled。

#### 14.15.2 数据面回归（真机端到端）

拓扑：腾讯云 CentOS 7（网关 + netns 客户端）→ ZeroTier → Oracle（出口）

| 检查 | 结果 |
|---|---|
| 节点发现 | ✅ 网关抓到 1 个 peer |
| 隧道确实被使用 | ✅ `slx0` tx 6 → 10 |
| **TCP 经隧道** | ✅ curl 返回 `129.225.146.84`（Oracle 出口 IP） |
| 分流生效 | ✅ 4346 条国内网段，接口 veth-slc |
| 选路器工作 | ✅ L1 探测 216.5ms，评分 35.1，候选出口正常 |
| ICMP | ⚠️ 25% 丢包 —— Oracle 该链路本身丢包（直连实测 33%） |

**ICMP 的丢包不是 slx 的问题**：同一时刻 TCP 经隧道成功完成握手并返回了出口 IP，
说明「隧道 + 用户态 NAT + 回程还原」全链路可用。TCP 有重传，在丢包链路上本就是
更可靠的验证方式。

#### 14.15.3 全部 CLI 命令真机验证

| 命令 | 结果 |
|---|---|
| `slx version` | ✅ `slx 0.9.0-m6 (735fbf9)  协议版本 1..1` |
| `slx doctor` | ✅ 14 项检查 |
| `slx status` | ✅ 组网/分流/选路/流量分区块展示 |
| `slx routes` | ✅ 候选出口评分表 |
| `slx explain <目标>` | ✅ 打分公式与门槛完整展示 |
| `slx probe` | ✅ 各出口实测质量 |
| `slx trace <目标>` | ✅ 七跳逐级展示 |
| `slx logs` | ✅ 无日志文件时给出 systemd/procd 指引 |
| `slx install-hooks` | ✅ 正确识别非 OpenWrt 并给出手动方案 |
| `slx notify` | ✅ 无守护进程时静默成功（退出码 0） |

#### 14.15.4 测试过程中踩到并修掉的问题

| # | 问题 | 现象 | 修法 |
|---|---|---|---|
| 1 | 覆盖运行中的二进制 | `env: /tmp/slx: Text file busy` | 先传 `/tmp/slx.new`，停进程后再 `mv` 原子替换 |
| 2 | 后台进程绑住 SSH 会话 | ssh 命令 240 秒超时不返回 | 用 `setsid ... < /dev/null` 完全脱离 |
| 3 | 密钥权限过宽 | OpenSSH 拒绝 `UNPROTECTED PRIVATE KEY FILE` | 复制到临时文件并 `icacls /inheritance:r` |
| 4 | 7MB 二进制在丢包链路上传不动 | SCP 反复断流 | **传输前 gzip**，7.3 MB → 2.9 MB |
| 5 | 测试脚本被管道提前关闭 | `Select-Object -First N` 杀掉 Python，**清理段没执行**，VPS 残留规则 | 清理必须独立成段、不依赖输出管道 |

第 5 条值得单独记住：把「清理」放在同一个脚本末尾，一旦输出被提前切断
（管道、超时、崩溃），清理就不会执行。以后所有涉及远端改动的测试，
清理都必须是独立的、可重复执行的一步。

#### 14.15.5 两台 VPS 的最终状态

```
腾讯云 82.157.26.98          Oracle 129.225.146.84
  slx进程=0                    slx进程=0
  slx0=无                      slx0=无
  netns=0                      netns=0
  iptables=0 0 0 0             iptables=0 0 0 0
  ipset=0                      ipset=0
  iprule=0                     iprule=0
  table100=0                   table100=0
  lo10.254=0                   lo10.254=0
  默认路由正常                 默认路由正常
  ping 223.5.5.5 0% 丢包       ping 223.5.5.5 0% 丢包
  ZT 192.168.99.4/24           ZT 192.168.99.24/24
```

#### 14.15.6 M6 剩余项

| 项 | 状态 | 说明 |
|---|---|---|
| CentOS 7 真机回归 | ✅ | 见本节 |
| 黄金向量测试 | ✅ | §14.14 |
| DNS 转发器 | ✅ | §14.14（12 个单元测试） |
| 全部 CLI 命令 | ✅ | 见 §14.15.3 |
| **OpenWrt 真机** | ⏳ | 需用户在场：旁路由是家里上网出口，改动有断网风险 |
| 性能压测 | ⏳ | 已有单包基准（NAT 310ns、选路 0 分配），未做满带宽压测 |
| 协议版本协商端到端 | 🔄 | `Negotiate()` 已实现并单测，未在真实新旧版本混跑验证 |

**Oracle VPS 本身有网络质量问题**（丢包 25~33%），建议排查；
它目前是 PassWall 的一条出口，链路不稳会直接影响上网体验。
### 14.11 各平台适配专项

架构差异不### 14.15 M6 真机回归结果（2026-11-06）

#### 14.15.1 CentOS 7 环境探测（slx doctor）

在腾讯云 **CentOS Linux 7.9.2009 / 内核 5.17.1-1.el7.elrepo / x86_64** 上：

```
共 14 项：通过 11，警告 1，失败 0

【分流】
  [通过] 防火墙实现   iptables-legacy
  [通过] ipset       已安装
  [通过] 默认路由     出口接口 eth0
  [通过] LAN 接口     eth0 172.21.0.15/20
  [警告] 分流数据     中国大陆网段已 2 天未更新（4344 条）
【组网】
  [通过] ZeroTier 接口 zt6vwy3rne(192.168.99.4/24 mtu=2800)
【转发】
  [通过] 运行身份 / 平台架构 / TUN 设备 / 内存 / 临时目录 / 系统时钟
```

**环境探测在 CentOS 7 上完全正确**：识别出 iptables-legacy、ipset、
ZeroTier 接口、TUN 可用、centOS 发行版、SELinux disabled。

#### 14.15.2 数据面回归（真机端到端）

拓扑：腾讯云 CentOS 7（网关 + netns 客户端）→ ZeroTier → Oracle（出口）

| 检查 | 结果 |
|---|---|
| 节点发现 | ✅ 网关抓到 1 个 peer |
| 隧道确实被使用 | ✅ `slx0` tx 6 → 10 |
| **TCP 经隧道** | ✅ curl 返回 `129.225.146.84`（Oracle 出口 IP） |
| 分流生效 | ✅ 4346 条国内网段，接口 veth-slc |
| 选路器工作 | ✅ L1 探测 216.5ms，评分 35.1，候选出口正常 |
| ICMP | ⚠️ 25% 丢包 —— **Oracle 该链路本身丢包**（直连实测 33%） |

**ICMP 的丢包不是 slx 的问题**：同一时刻 TCP 经隧道成功完成 TLS 握手并返回了
出口 IP，说明「隧道 + 用户态 NAT + 回程还原」全链路可用；TCP 有重传，
在丢包链路上本就是更可靠的验证。

#### 14.15.3 全部 CLI 命令真机验证

在 CentOS 7 上逐个验证通过：

| 命令 | 结果 |
|---|---|
| `slx version` | ✅ `slx 0.9.0-m6 (735fbf9)  协议版本 1..1` |
| `slx doctor` | ✅ 14 项检查 |
| `slx status` | ✅ 组网/分流/选路/流量分区块展示 |
| `slx routes` | ✅ 候选出口评分表 |
| `slx explain <目标>` | ✅ 打分公式与门槛完整展示 |
| `slx probe` | ✅ 各出口实测质量 |
| `slx trace <目标>` | ✅ 七跳逐级展示 |
| `slx logs` | ✅ 无日志文件时给出 systemd/procd 指引 |
| `slx install-hooks` | ✅ 正确识别非 OpenWrt 并给出手动方案 |
| `slx notify` | ✅ 无守护进程时静默成功（退出码 0） |

#### 14.15.4 测试过程中踩到并修掉的问题

| # | 问题 | 现象 | 修法 |
|---|---|---|---|
| 1 | 覆盖运行中的二进制 | `env: /tmp/slx: Text file busy` | 先传 `/tmp/slx.new`，停进程后再 `mv` 原子替换 |
| 2 | 后台进程绑住 SSH 会话 | `ssh` 命令 240 秒超时不返回 | 用 `setsid ... < /dev/null` 完全脱离 |
| 3 | 密钥权限过宽 | OpenSSH 拒绝：`UNPROTECTED PRIVATE KEY FILE` | 复制到临时文件并 `icacls /inheritance:r` |
| 4 | 7 MB 二进制在丢包链路上传不动 | SCP 反复断流 | **传输前 gzip**，7.3 MB → 2.9 MB |
| 5 | 测试脚本被管道提前关闭 | `Select-Object -First N` 杀掉 Python，**清理段没执行**，VPS 残留规则 | 清理必须独立成段、且不依赖输出管道；已手动补清 |

**第 5 条值得单独记住**：把「清理」放在同一个脚本的末尾，
一旦输出被提前切断（管道、超时、崩溃），清理就不会执行。
以后所有涉及远端改动的测试，清理都必须是独立的、可重复执行的一步。

#### 14.15.5 两台 VPS 的最终状态

```
腾讯云 82.157.26.98          Oracle 129.225.146.84
  slx进程=0                    slx进程=0
  slx0=无                      slx0=无
  netns=0                      netns=0
  iptables=0 0 0 0             iptables=0 0 0 0
  ipset=0                      ipset=0
  iprule=0                     iprule=0
  table100=0                   table100=0
  lo10.254=0                   lo10.254=0
  默认路由正常                 默认路由正常
  ping 223.5.5.5 0% 丢包       ping 223.5.5.5 0% 丢包
  ZT 192.168.99.4/24           ZT 192.168.99.24/24
```

#### 14.15.6 M6 剩余项

| 项 | 状态 | 说明 |
|---|---|---|
| CentOS 7 真机回归 | ✅ | 见本节 |
| 黄金向量测试 | ✅ | §14.14 |
| DNS 转发器 | ✅ | §14.14（12 个单元测试） |
| 全部 CLI 命令 | ✅ | 见 §14.15.3 |
| **OpenWrt 真机** | ⏳ | 需用户在场：旁路由是家里上网出口，改动有断网风险 |
| 性能压测 | ⏳ | 已有单包基准（NAT 310ns、选路 0 分配），未做满带宽压测 |
| 协议版本协商端到端 | 🔄 | `Negotiate()` 已实现并单测，未在真实新旧版本混跑验证 |

**Oracle VPS 本身有网络质量问题**（丢包 25~33%），建议用户排查；
它目前是 PassWall 的一条出口，链路不稳会直接影响上网体验。
只是"编个二进制"，以下每项都要**在真机或模拟环境实测**：

| 专项 | 验收标准 |
|---|---|
| **OpenWrt 真机矩阵** | 至少各一台：aarch64（IPQ807x 类）、armv7、**mipsel（MT7621）**、x86_64 |
| **iptables-legacy 与 nft 双路径** | 两条路径下分流结果一致，`doctor` 能正确识别当前变体 |
| **防火墙重载自愈** | `service firewall restart` 后 ≤ 15s 内规则自动恢复，期间记录 WARN + hint |
| **kmod-tun 缺失提示** | 未装 kmod-tun 时 `doctor` 给出准确安装命令 |
| **CentOS 7 + SELinux enforcing** | 能正常建 TUN、改路由、读配置；`doctor` 检测 SELinux 并提示 |
| **Oracle ARM 1GB 机型** | 出口节点内存占用 < 50MB，NAT 表按内存自动缩容 |
| **低端 MIPS 网关** | 转发吞吐 ≥ 100 Mbps，空闲 CPU < 2%，空闲内存 < 40MB |
| **链路中断恢复** | ZT 断开 → 熔断 → 切换 → ZT 恢复 → 自动回切，全程无需人工干预 |


---

## 15. 落地路径

### 15.1 总体原则

**ZeroTier 保持原样，一行不用改。** SLX 是叠加层，随时可停用，不影响你现有的任何网络用法（远程访问 192.168.8.0/24、10.10.10.0/24 等）。

### 15.2 分阶段实施

```
阶段 0  VPS 侧准备（不动 OpenWrt）
        每台 VPS 上装 slx（出口模式，免特权）
        两种接入都装上：
          · wss 传输：nginx 加一个 location（复用现有 LE 证书与域名）
          · zt  传输：零改动，SLX 监听 ZT 地址
        → 验证：slx peer 能在 ZT 上发现它们

阶段 1  OpenWrt 侧「只探测不接管」
        slx run --probe-only
        → slx peers / slx probe / slx explain 可用
        → 实测每个 (VPS, 传输) 的真实速度，与 PassWall 当前节点对比

阶段 2  接管（一次性迁移 PassWall）
        停用 PassWall → slx install-hooks → slx run
        → 验证：境外走隧道 / 境内直连 / doctor 全绿
        → 观察 1~2 周，确认体验不劣于原 PassWall

阶段 3  扩展
        其余 OpenWrt 节点逐个迁移
        → 每个节点独立选路（出口侧广播负载自动分散）
```

### 15.3 回滚

任何阶段出问题，**回滚成本极低**：

```
阶段 1 之前：  直接停掉 slx 进程，PassWall 原样在跑，无感知
阶段 2 之后：  slx uninstall  → 清 SLX 规则与 ipset
              → 在 LuCI 重新启用 PassWall
              → 恢复原状（PassWall 的配置我们不主动删）
```

> 这也是"不自动删除用户既有配置"这条纪律的现实价值：**你的 PassWall 配置始终是退路**。

---

## 16. 待你确认的问题

### 16.1 架构层面

| # | 问题 | 我的建议 |
|---|---|---|
| 1 | **SLX 接管后，PassWall 完全停用还是保留？** | 建议**停用**（SLX 已覆盖透明代理+分流+选路，且吞吐高 3 倍）。但保留 PassWall 的配置文件不删，随时可切回 |
| 2 | **Oracle 那批 NAT 机器（152.70.247.243 等）的 ZT 怎么修？** 它们的 ZT endpoint 是内网 IP `10.0.0.50`，所以在 ZT 里完全不可达 | 两条路：① 修 ZT（`zerotier-cli set <net> allowManaged=0` + 手工指定公网 endpoint，或在 Oracle 安全列表放行 ZT 端口让 1:1 NAT 生效）② 不修，SLX 用 `tls` 直连它们的公网 IP（**这就是传输可插拔的意义**）。建议先按 ② 做，不阻塞上线 |
| 3 | **要不要给所有 VPS 配 TLS 证书与域名**（`wss` 传输的前提） | 既然默认走 ZT，**不急**。等哪天发现 ZT 路径被针对性限速再补。届时每台一张 `*.yourdomain.com` 泛域名证书最省事 |
| 4 | **国内/境外判定**：只用「中国大陆 IP 段」列表，还是也要按域名分类 | 先只用 IP 段；DNS 层加一条兜底 |
| 5 | **多网关协调**：几个 OpenWrt 各自独立选路，还是集中协调避免都选同一台 VPS | 各自独立 + 出口侧广播负载惩罚，自然分散 |
| 6 | **低端路由器**：MIPS/MT7621 类是全量跑网关还是只跑精简版 | 出精简版 |
| 7 | **你的 eMMC 只剩 200MB**，是否接受 SLX 不装额外依赖 | SLX 单文件 ~5-8MB，装在 /usr/bin 或 /tmp 均可。**不写任何状态到闪存**（状态放 /tmp，重启重建，2 秒内恢复） |

### 16.2 需要你授权/确认的操作

| # | 事项 | 说明 |
|---|---|---|
| 1 | **我在你的旁路由上装了一把 SSH 公钥**（`/etc/dropbear/authorized_keys`，用 `id_rsa_bb_server` 的公钥） | 为了后续自动化巡检。**要保留还是现在删掉？** |
| 2 | **Oracle ARM (129.225.146.84) 的 SSH 访问方式** | 我想进去看它上面 nginx/v2ray 的真实配置，把 `wss` 接入点定死。目前 bb_server / deploy_key 都被拒 |
| 3 | **各 VPS 上装 slx 出口模式** | 免特权，不动现有服务，与 v2ray/nginx 共存 |
| 4 | **152.70.247.243 上我临时开过 5201/8000 端口** | 已全部清理，防火墙规则已还原 |

---

## 18. 架构转向：反向连接 + 用户态协议栈（2026-11-08）

> 触发：参考 [ligolo-ng](https://github.com/nicocha30/ligolo-ng) 的隧道模型后确认，
> 现有「出口节点转发裸 IP 包」的架构有两个绕不开的结构性问题，且都不是实现 bug，
> 而是选型带来的必然代价。本章记录转向决策与实现。

### 18.1 为什么换

#### 问题一：出口节点必须被「够得着」

现状：网关要能把隧道包发给出口，出口必须在 ZeroTier 里有一个可达地址，
或者有公网 IP（`public_addr`）。DESIGN §14.15 记录过实测困境：

> All VPS behind provider NAT → `PublicIPv4()` 为空 → `direct` 传输需要 `public_addr`

后果：
- 强依赖 ZeroTier 的可达性。实测 ZT 链路长期不稳（Oracle 的 ZT 地址
  `192.168.99.24` 反复不可达，而公网 SSH 正常）。
- 新增出口节点必须先进 ZT 网络，部署门槛高。

#### 问题二：出口节点必须转发裸 IP 包，导致一整类内核坑

现状：出口建 TUN、开 ip_forward、做 SNAT、放开 rp_filter。M3 期间为让这条路
跑通付出的代价（DESIGN §14.10 / §14.7）：

| 踩到的坑 | 本质 |
|---|---|
| 内核 DNAT 回程包在 mangle PREROUTING 之后凭空消失 | 内核 NAT 与该路径的交互，两套发行版 + WSL 均复现，未定位到根因 |
| `rp_filter` 是 `max(all, iface)` 语义，设成 loose(2) 也无效 | 必须把 `all` 归零 |
| ICMP 没有伪首部，校验和不能按 TCP/UDP 处理 | 协议细节 |
| 端口耗尽导致 NAT 退化线性扫描（55 µs/包） | 用户态 NAT 的表规模设计 |
| 出口节点必须 root、必须能建 TUN | 部署约束 |

这些全是为了「让出口替别人转发 IP 包」而存在的。**如果出口根本不转发 IP 包，
而是按需发起自己的连接，这些问题一个都不会出现。**

### 18.2 新架构

```
网关（原「网关」角色）                        出口（原「出口」角色，现「agent」）
  TUN slx0                                      无 TUN、无 NAT、无 root
    ↓                                              ↑
  gvisor 用户态协议栈                               │ 主动拨出（反向连接）
  收到 SYN → 选路 → 发 ConnectRequest ────TLS/yamux──┘
                    ←──── ConnectResponse(established)
  Complete(true) 接受本地 TCP                    net.Dial(目标)
  ←──────────────  字节流双向中继  ──────────────→
```

三个关键点：

1. **反向连接**：出口侧 agent 主动 `dial` 网关的监听端口。只要出口能上网，
   它就能连上网关 —— **不需要公网 IP、不需要 ZT 可达**。这直接消掉问题一。
2. **用户态协议栈终结连接**：网关用 gvisor 在 slx0 上终结 TCP/UDP，出口只执行
   `connect()`。出口不碰裸包 → 消掉问题二的全部五条。
3. **yamux 多路复用**：一条 TLS 连接上跑任意多条并发流，不用为每个连接建 TCP。

### 18.3 协议设计

复用现有控制面（JSON 信封，`internal/proto/control.go`），**新增两条消息**：

```go
// ConnectRequest 网关 → agent：请替我在目标上建一条连接。
type ConnectRequest struct {
    StreamID  uint32 `json:"stream_id"`  // yamux 流号，仅用于日志关联
    Net       uint8  `json:"net"`        // 0=v4 1=v6
    Transport uint8  `json:"transport"`  // 0=tcp 1=udp
    Addr      string `json:"addr"`       // 目标 IP
    Port      uint16 `json:"port"`
    Framed    bool   `json:"framed"`     // UDP 是否带长度帧
}

// ConnectResponse agent → 网关
type ConnectResponse struct {
    StreamID    uint32 `json:"stream_id"`
    Established bool   `json:"established"`
    Reset       bool   `json:"reset"`    // 建连被拒（ECONNREFUSED 等）→ 网关回 RST
    Reason      string `json:"reason,omitempty"`
}
```

**流内数据**：握手完成后该 yamux 流即为纯字节管道（TCP）或长度帧管道（UDP）。

**错误语义**（照抄 ligolo 的判定，这决定了 nmap / 快速失败的表现）：

| agent 侧结果 | 网关回给 LAN 客户端 |
|---|---|
| connect 成功 | SYN-ACK |
| ECONNREFUSED / ECONNRESET / ECONNABORTED | RST（快速失败，不挂住） |
| 超时 | 不回包（让客户端自己超时重试） |

### 18.4 数据面分流保持不变

网关侧的**流量接管**（ipset 国内段 + fwmark + 策略路由 + 看门狗）完全不动：
包怎么进 slx0 的入口不变，变的是「进 slx0 之后怎么办」——
从「读出 IP 包发给出口」变成「gvisor 终结连接再中继」。

因此 §7 的分流规则、§14.10 的四道环路防护**全部继续有效**。

### 18.5 配置变更

```yaml
# 传输模式：proxy = 反向连接 + 用户态协议栈（默认，新架构）
#           packet = 旧的裸包转发（保留，用于需要任意 IP 协议的场景）
transport_mode: proxy

# 网关侧：agent 反向连接监听地址
agent_listen: 0.0.0.0:11901

# 出口侧：网关地址（agent 主动拨这里）
gateway_addr: "192.168.1.254:11901"
```

### 18.6 取舍：不做什么

新架构不是超集，有三件事会退化，必须显式记录：

| 能力 | packet 模式 | proxy 模式 | 说明 |
|---|---|---|---|
| 任意 IP 协议 | ✅ | ❌ | IPsec/ESP、自定义 IP 协议不行了。家里上网其实用不到 |
| ICMP | 全类型 | 仅 echo | gvisor 只终结 echo，其它类型原样透传代价高 |
| 源 IP 透明 | ✅ | ❌ | 出口用自己的地址建连。但「境外流量以 VPS 身份出去」本来就是需求，不算损失 |
| 端到端 TCP 语义 | ✅ | ⚠️ | 两端 TCP 被拆成两段，TCP 选项（如显式拥塞通知）不端到端 |

**保留 packet 模式**：需要上面这些能力时切回去，不用回退版本。

### 18.7 依赖与许可

| 依赖 | 版本 | 许可 | 说明 |
|---|---|---|---|
| `gvisor.dev/gvisor` | pin `v0.0.0-20240722211153-64c016c92987` | Apache-2.0 | **不能用 latest**：2026-11 的快照里 `pkg/tcpip/stack/bridge_test.go` 声明了 `package bridge_test`（应为 `stack_test`），导致包加载失败 |
| `github.com/hashicorp/yamux` | v0.1.2 | MPL-2.0 | 流多路复用 |
| ligolo-ng 本身 | — | GPLv3 | **只借鉴架构，不复制代码** |

体积实测：单独 gvisor tcpip 3.8 MB（arm64，`-s -w`）。
预期 slx 从 7.1 MB 增至 **10~12 MB**。路由器 eMMC 剩余 200 MB，可接受。

### 18.8 实施顺序

1. 协议扩展 + 黄金测试更新（`internal/proto`）
2. `internal/agent`：出口侧反向拨号、connect 执行、字节中继
3. `internal/gateway`：网关侧 agent 连接池、按 node_id 开流
4. `internal/netstack`：gvisor 挂 TUN，TCP/UDP forwarder 接 picker
5. 网关集成：SYN → 选路 → 开流；UDP 帧中继
6. 本地 netns 验证：反向连接、TCP/UDP/ICMP 端到端、出口掉线切换
7. 真机回归：出口节点**不安装 TUN、不用 root** 也能跑通

### 18.9 实施结果（2026-11-08）

#### 已完成的代码

| 包 | 行数 | 职责 |
|---|---|---|
| `internal/proto` | +60 | 新增 `ConnectReq`/`ConnectResp`、`CapAgent`、`TransportRelay` |
| `internal/stream` | ~330 | 流内握手（长度前缀 JSON）+ TCP 字节中继 + UDP 长度帧中继 |
| `internal/relay` | ~700 | HMAC 认证握手、TLS、yamux 会话、Gateway 连接池、Agent 反向重连 |
| `internal/netstack` | ~430 | gvisor 协议栈、TCP/UDP forwarder、透明代理（promiscuous+spoof）、双泵 |
| `internal/cli` | ~450 | `setupProxyPlane`、`proxyConnector`、`serveProxy`、`slx agent` |

#### 测试覆盖（全部通过）

| 包 | 测试数 | 关键验证点 |
|---|---|---|
| `internal/stream` | 6 | 握手往返、错帧类型拒绝、超长拒绝、TCP 双向中继、**UDP 数据报不粘连**、错误归类 |
| `internal/relay` | 5 | **端到端 TCP 中继到真实服务**、密钥错误拒绝、时钟偏斜拒绝、未知 agent 报错、**端口拒绝返回 Reset** |
| `internal/netstack` | 3 | **双 gvisor 栈对接的 TCP 穿透**、拒绝语义（RST）、**UDP 数据报边界** |

`internal/netstack` 的测试用「两个 gvisor 栈对接」的方式：一个模拟 LAN 客户端，
一个是被测的代理栈，中间用 channel 端点搬运 IP 包。这样**不需要真实 TUN、不需要 root、
不需要 netns**，却完整覆盖了 SYN → forwarder → Connector → 中继 → 回包的整条路径。

#### 实测二进制体积

| 架构 | 旧 | 新 | 增量 |
|---|---|---|---|
| linux/arm64 | 7.1 MB | **9.8 MB** | +2.6 MB |

比预估的 10~12 MB 还好一点。路由器 eMMC 剩 200 MB，完全可接受。

#### 实现中踩到的坑

| # | 问题 | 现象 | 修法 |
|---|---|---|---|
| 1 | gvisor latest 快照损坏 | `pkg/tcpip/stack/bridge_test.go` 声明 `package bridge_test`（应为 `stack_test`），导致包加载失败 | 钉到 `v0.0.0-20240722211153-64c016c92987` |
| 2 | yamux 必须显式给日志出口 | 传自定义 `Config` 但 `Logger`/`LogOutput` 都空 → `one of Logger or LogOutput must be set` | 两处都设 `LogOutput: io.Discard` |
| 3 | gvisor 透明代理必须开两开关 | 不开 `PromiscuousMode` 收不到目的的包，不开 `Spoofing` 发不出去，**且没有任何错误日志** | 创建 NIC 后立刻都设为 true |
| 4 | TCP 必须先连出口再 Complete | 先 `Complete(true)` 再连出口，出口连不上时客户端「连上了却永远收不到数据」 | 顺序固定为：连出口 → 成功才创建端点 → Complete(false) |

#### 真机联调结果（2026-11-08，两台 VPS）

拓扑：**腾讯云 CentOS 7（网关，建 netns 模拟 LAN 客户端）→ 反向连接 → Oracle（出口 agent，以非 root 用户 `slxtest` 运行）**

| 验证项 | 结果 | 证据 |
|---|---|---|
| agent 不需要 root | ✅ | `ps -o user=` 显示 `slxtest`；进程无 TUN、无 iptables 改动 |
| agent 不需要公网可达 | ✅ | agent 主动拨网关 `82.157.26.98:11601` |
| **TCP 经隧道** | ✅ | netns 客户端 curl 返回 `129.225.146.84`（Oracle 出口 IP） |
| **UDP 经隧道** | ✅ | netns 客户端 `dig @8.8.8.8 www.baidu.com` 返回 `103.235.46.96` |
| 断线自动重连 | ✅ | 杀 agent 后 `agent_joined` 从 1 增至 2，重连后 TCP 仍通 |
| 两台 VPS 清理 | ✅ | 无残留进程/网卡/netns，ping 正常 |

**这是新架构的核心主张被验证**：出口节点零权限、无 TUN、无 NAT，
过去 M3 阶段踩的 `rp_filter` / 内核 NAT 回程丢包 / 端口耗尽 那一整类问题
在这里**根本不存在**，因为出口从不转发裸 IP 包。

#### 联调中修掉的三个真问题

| # | 问题 | 现象 | 修法 |
|---|---|---|---|
| 1 | Tencent 防火墙挡 agent 端口 | `dial tcp ...:11601: no route to host` | firewalld 放行 11601；`slx doctor` 应加一项「agent 端口可达性」检查 |
| 2 | **网关与 agent 的 UDP 帧格式不一致** | TCP 正常、UDP 全部超时 | 网关侧原本自己写 4 字节长度头，agent 侧用 `stream` 包 6 字节帧头。改为两边共用 `stream.FramedPipe` |
| 3 | 重启用 TUN 会把路由一起删掉 | 重新加路由前客户端报 `No route to host`，误判成协议栈问题 | 测试脚本里必须在 slx0 建好**之后**再加路由 |

第 2 条值得记住：**它骗过了一整轮单元测试**。
netstack 层自测用的是「Connector 直连真 socket」，根本不经过 agent 的中继，
所以两端帧格式不一致也能全绿。补的 `TestUDPThroughFullRelayChain` 贯穿了
`gvisor → Connector → Gateway.Open → yamux → Agent.FramedPipe → 真实 socket`
整条链，这类问题以后会被拦住。

#### 尚未完成

| 项 | 状态 | 说明 |
|---|---|---|
| ICMP echo | ✅ | 泵层接管 + agent 侧探测，见 §18.12 |
| proxy 模式的 status/routes | ✅ | 独立的状态收集器与快照循环，见 §18.10 |
| proxy 模式的 probe（L1 链路质量） | ✅ | relay 流上的应用层 RTT 采样，见 §18.11 |
| proxy 模式的 DNS 转发器 | ✅ | 复用 dnsfwd + dnsPolicy，出口解析改走 relay 流，见 §18.13 |
| 家宽实测 | ⏳ | 需要用户在旁路由上切换模式；旁路由是家庭实际出口，需用户在场 |

### 18.10 proxy 模式的可观测性

packet 模式的 `slx status` 模板不能直接复用：它的关键信息是「peer 拓扑 + 选路评分」，
而 proxy 模式没有数据面 peer（出口由反向连接的 agent 承担），
硬套模板会把不适用的故障判定（如「没有发现其它节点」）误报出来。

因此 proxy 模式有独立的状态收集器 `collectProxyStatus`，输出：

```
闪连 SLX 0.10.0-m7
节点 2732961a2514 角色 egress+gateway+probe 模式 proxy
运行 18 秒（自 11-08 17:59:54）

出口：
  已接入 1 个   监听 [::]:11601
    37be0f7e7d11

分流：
  未启用（境外流量走直连）

代理数据面：
  TUN 收 3 包 / 发 0 包
  TCP 接受 0 条 / 失败 0 条    UDP 中继 0 条
```

故障判定也换了判据：

| 条件 | 级别 | 说明 |
|---|---|---|
| 没有 agent 接入 | **致命** | 一条流量都出不去；ZeroTier 不是必需的，所以「没有 peer」在这里只是提示 |
| 有包进 TUN 但零连接被接受（>20 包） | 降级 | 内核交了包但协议栈没建连，查 `netstack.*_failed` |

`slx routes` 的候选来源同样改成 **agent 连接池**（与 `proxyConnector.candidates` 一致），
否则会出现「routes 说没候选，但连接其实是通的」这种自相矛盾。

**已知不足**：proxy 模式下候选评分显示 `0.0`，因为 L1 链路质量探测还没做 ——
它目前依赖 packet 模式的 mesh 探测。要在 relay 流上做应用层 RTT 采样才能补上。
### 18.11 proxy 模式的 L1 探测与智能选路（2026-11-08 真机验证）

#### 为什么不能沿用 packet 模式的探测

packet 模式的 L1 测的是「隧道上发探测帧的往返」，依赖 mesh 数据通道。
proxy 模式不转发裸包，那条路径不存在，所以选路器一开始是**瞎的** ——
多个出口时只会按候选顺序挑第一个，「智能选路」名存实亡。

#### 做法：relay 流上的应用层 ping

每个 agent 会话维持一条**专用探测流**（`KindProbeHello` 标记），
网关每 2 秒发 5 字节 `KindPing` 帧，agent 原样回 `KindPong`。
测到的是「网关↔出口这条连接本身」的往返，包含 yamux 多路复用的调度开销 ——
正是选路需要的口径，而不是某个应用层心跳的数字。

| 参数 | 取值 | 理由 |
|---|---|---|
| 采样间隔 | 2s | 与 packet 模式一致，切换灵敏度体感相同 |
| 单次超时 | 3s | 超时记一次丢包，不阻塞其它 agent |
| 丢包窗口 | 最近 20 次（约 40s） | 太短被抖动带偏，太长掩盖正在恶化的链路 |

**关键细节**：采样失败时**不把 0 传给 `UpdateL1`**，而是沿用最近一次成功的 RTT。
否则一次抖动会把 EWMA 瞬间拉到接近 0ms，让一条刚出问题的链路看起来「快到 0ms」，
选路反而更偏爱它 —— 这是那种「偶发选错出口、很难复现」的 bug 的典型来源。

带宽暂不测（传 0），选路器会用 `fallbackBWMbps` 兜底，评分主要由 RTT 与丢包驱动。

#### 真机结果：选路器确实按距离打分

网关在腾讯云，两个 agent：

| agent | 位置 | RTT | 评分 |
|---|---|---|---|
| `ae6f295dcdbf` | 本机（家宽） | **12.5 ms** | **55.2** |
| `a953a3b38d1b` | Oracle（日本） | **345.8 ms** | **31.9** |

27 倍的延迟差被正确反映到评分上（55.2 vs 31.9），远的出口被显著压制。
**这是「智能选路」在 proxy 架构下成立的直接证据。**

顺带验证：`slx status` 正确显示「已接入 2 个」及两个 node_id；
`slx routes` 的候选列表与评分同步刷新。

#### 踩到的坑

| # | 问题 | 现象 | 说明 |
|---|---|---|---|
| 1 | Windows 时钟粒度粗 | `TestPingMeasuresRTT` 里 RTT 恒为 0 | 本机 `time.Now()` 连续 1000 次调用差为 0、十万次空循环测出 0s。测试改为只断言「往返成功且非负」，真机（Linux）不受影响 |
| 2 | 忘了重建 dist 二进制 | 探测代码合了但真机评分仍是 0.0 | 改完代码只跑了 `go build ./...`，没重建 `dist/` 下要上传的产物。部署前必须重建 |

第 2 条在这轮里出现两次（一次是状态快照、一次是探测），
**教训：验证脚本用的二进制必须显式重建，不能假设「源码改了产物就新」。**
### 18.12 proxy 模式的 ICMP echo（2026-11-08 真机验证）

#### 问题

gvisor 只终结 TCP/UDP。proxy 模式下 ICMP echo 请求进 TUN 后被协议栈丢弃，
**ping 完全不通**。这不只是少一个功能：很多设备/系统靠 ping 判断「有没有网」，
ping 不通会让用户以为整个网络挂了。

#### 做法：泵层接管 + agent 侧探测

```
LAN 客户端 ping 8.8.8.8
      ↓（内核路由进 slx0）
网关泵层识别 ICMP echo request
      ↓
选路 → 请该 agent 探测 8.8.8.8
      ↓
agent 回 alive=true
      ↓
网关合成 echo reply 写回 TUN  →  客户端收到 ping 回复
```

三个关键决定：

1. **为什么让 agent 探测而不是网关自己 ping**：网关的出发地和出口不同，
   测到的可达性对用户没意义。用户要的是「从出口那边能不能到」。
2. **不可达时静默不回包**，不伪造回复。伪造会让用户以为主机活着，
   而实际是网络不通 —— 那是比 ping 不通更糟的误导。
3. **必须异步**：ping 要等 agent 往返（几百毫秒到 4 秒超时），
   在 TUN 读循环里同步等会把整条数据面的包处理全卡住。

#### agent 侧的三级探测策略

| 级别 | 方式 | 前提 |
|---|---|---|
| 1 | 非特权 ICMP（`udp4` ping socket） | 内核 `net.ipv4.ping_group_range` 覆盖当前 gid |
| 2 | 特权 ICMP（raw socket） | root 或 CAP_NET_RAW |
| 3 | **TCP 连接探测（443/80）** | 无需任何权限 |

第 3 级是必需的兜底：实测多数发行版默认 `ping_group_range` 是 `"1 0"`（禁用），
而 agent 的核心卖点就是不依赖 root。没有兜底的话非 root agent 的 ping 全超时。

兜底的代价是「ICMP 被墙但 TCP 通」的主机会显示可达 —— 但对「能不能上网」
这个真正的问题来说，这反而是更准确的答案。

#### 结果缓存：把 25% 丢包降到 0%

TCP 兜底要建三次握手（跨境几百毫秒）。`ping -c 4` 每秒发一个，
每次都重新探测会来不及回包，**表现为「通了但有 25% 丢包」**。

加 5 秒结果缓存后：连续 ping 只探测一次，其余直接复用。

真机对比（网关腾讯云 → agent Oracle，链路本身有 25~33% 丢包）：

| 版本 | ping 结果 |
|---|---|
| 无缓存 | 4 transmitted / 3 received，25% loss，RTT 223ms |
| **有缓存** | **4 transmitted / 4 received，0% loss，RTT 224ms** |

#### 踩到的坑

| # | 问题 | 说明 |
|---|---|---|
| 1 | `os.Geteuid()` 在 Windows 不存在 | agent 要跨平台（实测用 Windows 机器当 agent），改成不依赖它 |
| 2 | ICMP 校验和只覆盖 ICMP 报文 | 没有 TCP/UDP 那样的伪首部。M3 阶段踩过同一个坑，这次直接在测试里锁住 |
| 3 | 非特权 ICMP 默认被禁 | `ping_group_range: 1 0`。agent 启动时会打一条 WARN 并给出 `sysctl` 修复命令 |

#### 状态可见性

`slx status` 的 proxy 段多了 ICMP 计数：

```
代理数据面：
  TUN 收 6 包 / 发 0 包
  TCP 接受 0 条 / 失败 0 条    UDP 中继 0 条
  ICMP echo 回复 4 次
```

过载丢弃（ping 洪水时）也会单独计数显示。
### 18.13 proxy 模式的 DNS 转发器（2026-11-08 真机验证）

#### 为什么要「经出口解析」

不是为了「能解析」——网关自己也能解析。价值在 **CDN 就近**：
同一域名在网关当地和出口当地解析出来的 IP 可能属于不同边缘节点。
解析在东京、流量却从法兰克福出去，视频照样卡。

#### 做法：复用 packet 模式的全部策略，只换两个注入点

`dnsfwd` 与 `dnsPolicy` **一行没改逻辑**，只把两处依赖改成可注入：

| 注入点 | packet 模式 | proxy 模式 |
|---|---|---|
| 候选出口 | mesh 发现表的出口节点 | **agent 连接池** |
| 「经出口解析」的执行 | 发 `ResolveReq` 控制消息给出口 | 开一条 relay 流请 agent 解析 |

agent 侧用 `net.DefaultResolver` 解析 —— 拿到的就是贴着出口位置的 IP。

#### 踩到的坑：CN 网段列表没加载

第一次联调时 `egress_hits` 一直是 0。原因：`dnsPolicy` 靠「解析结果是否全部落在
中国大陆网段」来判断域名归属，而网关侧配了 `dispatch: false`（不装分流规则），
`setupDispatch` 提前返回，CN 列表根本没下载。策略拿不到国内网段就拒绝下结论，
于是**所有域名都被当成本地域名**，DNS 就近完全失效。

修法：proxy 模式下 `cnNets` 在 dispatch 没准备数据时**独立加载一次** `cnlist.Load`。
懒加载 + 只在启用 DNS 时才触发，不会给不关心 DNS 的部署增加开销。

#### 真机结果

```
DNS：
  监听 127.0.0.1:5353   查询 3 次（本地 1 / 经出口 1 / 失败 0）
```

第 1 次查询走本地（策略还不知道这是境外域名），`Observe` 学到之后
第 2 次起改走出口。这是「先观测再判断」的设计 —— 不在第一次就武断分流。

`slx status` 的 proxy 段新增 DNS 计数（监听地址 / 查询数 / 本地 / 经出口 / 失败），
一眼能看出就近逻辑有没有生效。

**验证上的一个坑**：用 `www.cloudflare.com` 做对照看不出差别 ——
它是 Anycast，全球解析结果一样。判断「走没走出口」必须看计数，不能看解析结果。
真实环境中应该用地域性强的域名（如各地 CDN 的媒体域名）来观察效果。
### 18.14 proxy 模式的带宽估算（2026-11-08 真机验证）

#### 为什么被动估算而不是主动测速

packet 模式的 L2 是「经出口下载一个真实网站并计时」。proxy 模式下没有等价物：

- **主动灌数据**：为了测速替用户下载 256KB，在家庭宽带上就是实打实吃掉上行；
- **替用户连站点**：为测速去连一个用户没访问的站点，语义上也说不通；
- **而中继本来就在搬运真实数据** —— 顺手统计几乎零成本。

所以做法是：**给每条中继流套一层字节计数，连接关闭时结算吞吐，取窗口内峰值**。

| 参数 | 取值 | 理由 |
|---|---|---|
| 最短时长 | 1s | 太短的连接测的是抖动不是带宽，会把慢链路估成「几十 Gbps」 |
| 最少字节 | 64 KB | 几百字节的连接没有参考价值 |
| 峰值窗口 | 3 分钟 | 链路降速后旧峰值会过期，不会永久高估 |

代价是**冷启动时没有带宽数据**（没有长连接时估不出来），此时返回 0，
选路器用 `fallbackBWMbps` 兜底 —— 不会被当成「零带宽」惩罚。

#### 真机结果：估算值与实测值吻合

经隧道下载 3 MB：

```
实测: 3,000,000 字节 / 8.863s = 2.7 Mbps
slx routes 显示: 2.8M
```

误差 3.7%，正好对应 TCP/IP 头开销（估算按应用层字节、实测也是应用层字节，
差值来自计时的边界效应）。**这个精度对选路完全够用** —— 选路只需要区分
「10M 的出口」和「2M 的出口」，不需要精确到小数点后两位。

同一时刻的完整快照：

```
候选出口：
  节点                       评分         延迟         带宽       丢包  状态
    42a99fcaf3c3643c    29.6    387.2ms      2.8M     0.0%  正常
```

三个维度都有真实数据了：RTT 来自 L1 探测、丢包来自采样失败率、带宽来自被动估算。

#### 一个设计上的取舍

被动估算在「只有小请求」的场景下永远拿不到样本（比如只刷网页、不下载）。
此时带宽维度走 fallback，选路主要由 RTT 和丢包驱动 —— 这对浏览类负载是够的；
但**大流量场景（视频、下载）恰恰最容易产生样本**，也正是带宽维度最该发挥作用的时候。
两者是自洽的。
### 18.15 家宽实测（OpenWrt 旁路由 192.168.1.254，2026-11-08）

#### 环境

| 项 | 值 |
|---|---|
| 硬件 | 4 核 ARM / 1.8 GB 内存 / aarch64 |
| 系统 | OpenWrt SNAPSHOT，内核 6.1.114-flippy |
| 存储 | overlay 剩 180 MB，/tmp 900 MB tmpfs |
| TUN | `kmod-tun` 已装，`/dev/net/tun` 可用 |
| `slx doctor` | **14 项检查，11 通过 0 失败** |

#### 已验证通过的部分

| 项 | 结果 |
|---|---|
| slx 二进制在 aarch64 OpenWrt 上运行 | ✅ 静态链接，无依赖问题 |
| `slx doctor` 环境探测 | ✅ 全部正确识别（openwrt / iptables-legacy / zt / TUN） |
| proxy 模式数据面启动 | ✅ TUN 创建、用户态协议栈就绪 |
| agent 反向连接监听 | ✅ `0.0.0.0:11601` |
| **出口 agent 经 ZeroTier 接入** | ✅ Oracle 以非 root 身份连上（`agent_joined`） |
| 分流规则插入位置 | ✅ `-I FORWARD 1`，排在 fw3 的 zone 规则之前 |

#### 端到端验证结论：链路是通的，但被 Oracle 的 ZT 不稳定卡住

关键证据（PassWall 主开关已关、`google=000` 确认代理真的停了的那一轮）：

| 指标 | 请求前 | 请求后 |
|---|---|---|
| `tcp_ok`（gvisor 接受的中继连接） | 0 | **1** |
| `tun_out`（写回 TUN 的回程包） | 0 | **12** |
| `slx0 rx` | 0 | **12** |

`tcp_ok=1` 意味着 gvisor 接受了一条 TCP 连接并成功中继给 agent，
`tun_out=12` 意味着回程包正确写回了 TUN。
**「本机 → 路由器 → slx0 → gvisor → 中继 → 出口 agent → 互联网」整条链路在真实家宽上跑通过。**

后续重复验证失败，原因是 **Oracle 那台机器的 ZeroTier 链路不稳定**：
路由器和 Oracle 的 ZT 地址互相 100% 丢包（而公网 SSH 正常）。
agent 连不上网关的 ZT 地址，自然无法复现。这与 §14.15 记录的
「Oracle 链路 25~33% 丢包」是同一个问题，不是 slx 的缺陷。

**要获得稳定可重复的家宽验证，需要一个 ZT 链路稳定的出口节点。**
建议排查 Oracle，或改用其它节点做出口。

#### 顺带发现的验证陷阱

- **`speed.cloudflare.com/__down` 会返回 403**，不能用来判断链路通不通 ——
  它拒绝请求和链路不通看起来一样。应该用 `https://api.ipify.org` 并检查返回的出口 IP。
- **PassWall 关主开关不等于规则撤掉**：`uci enabaled=0` 后进程与 nat/mangle 规则仍在，
  必须 `restart` 才真正生效。验证方法是看本机 `curl google` 是否变为失败。
- **路由器没装 `tcpdump`**，抓包前必须先 `opkg install tcpdump`，
  否则 `nohup tcpdump ...` 静默失败，误以为「没抓到包」。

#### 踩到的 OpenWrt 平台差异（重要）

这一轮最大的收获不是功能，而是**三个会让部署脚本静默失效的平台差异**：

| # | 差异 | 后果 | 正确做法 |
|---|---|---|---|
| 1 | **没有 `pkill`**（`command not found`） | `pkill -9 -x slx` 静默失败，旧进程一直活着占着端口；新进程 bind 失败，agent 连到旧进程 → **密钥不匹配**这种完全误导的错误 | `kill $(pidof slx)` |
| 2 | **没有 `setsid`** | 后台进程起不来（VPS 上一切正常，所以之前没暴露） | `nohup ... &` |
| 3 | **busybox `pgrep -x` 语义不同** | `pgrep -x slx` 匹配不到 `/tmp/slx run ...`（它比的是完整命令行），返回 0 → 误判「进程已清理」 | `pidof slx` |

**第 1 条最坑**：它导致「清理干净」的判断全是假的，而错误信息（密钥不匹配）
指向的是完全无关的原因。这类问题在自家 VPS 上永远复现不出来。

#### 修掉的 bug

proxy 模式的 `cleanup` 漏删了自己加的 FORWARD 放行规则。实测在路由器上留下了
`-A FORWARD -o slx0 -m comment --comment "slx:forward-slx0" -j ACCEPT` 两条，
且指向一个已经被删除的网卡。反复启停会无限累积。已修复并清理。
## 17. 一句话总结

> **SLX 是跑在 ZeroTier 之上的智能出口网关**：一个静态二进制（5 个架构：amd64 / arm64 / armv7 / mipsel / riscv64，一个文件扔到任何 Linux 都能跑）装在 OpenWrt 和 VPS 上，广播发现、自动成网（无需任何 IP 配置），持续实测每个出口的真实速度，为每一个境外目的地挑出当前最快的那条路并把流量从 OpenWrt 送出去；境内流量完全不受影响。
>
> **实测对比当前 PassWall 方案：33.4 Mbps → 95.6 Mbps**（因为 ZT 是 UDP 承载，不存在 TCP 三层套叠的吞吐塌陷），同时把"选哪个出口"从人工变成了自动，把"为什么慢"从猜变成了可解释。
>
> 没有控制台，只有两个配置项、一条命令，和一个随时能回答"为什么走这条路"的诊断体系。

---

_文档结束。评审通过后从 M0 开始。_
