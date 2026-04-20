# 闪连 VPN — 服务端开发计划 v0.0.0

**版本：v0.0.0**
**更新：2026-04-17**
**负责人：Go + Rust 开发团队**
**预计工期：28-32 天**

---

## 开发进度摘要（2026-04-17）

| 模块 | 状态 | 说明 |
|------|------|------|
| C1 项目初始化 | ✅ 完成 | Go 项目结构、中间件、错误码封装 |
| Settings API | ✅ 完成 | 平台配置、SMTP、Webhook、证书管理 |
| WebSocket 事件广播 | ✅ 完成 | 事件总线 + WebSocket Hub 集成 |
| IPAM (Peer IP) | ✅ 完成 | Peer IP 重新分配、网络 IP 分配 |
| 网络审计日志 | ✅ 完成 | operatorID 正确传递 |
| C2 Moon 注册心跳 | ✅ 完成 | HMAC认证、API Key轮换、心跳超时检测、moon_count/shutdown响应 |
| C3 Planet 注册心跳 | ✅ 完成 | 设备认证、IP池分配、network_info/policy_list/moon_list响应 |
| C4 IPAM | ✅ 完成 | IP释放、分配历史记录 |
| C5 流量统计 | ✅ 完成 | 定期聚合、流量配额检测 |
| C6 版本查询 | ✅ 完成 | 数据库查询支持 |
| Moon 节点 | ✅ 完成 | Rust 项目创建、TCP/UDP/WSS relay、SLNX协议、ChaCha20加密、Session管理、NAT检测、健康检查 |
| 审计服务器 | ✅ 完成 | Go 项目创建、日志接收、SQLite存储、告警引擎、转发管理 |

---

## 一、模块概述

服务端包含三大核心组件：

| 组件 | 语言 | 说明 |
|------|------|------|
| Controller API | Go | 信令服务器，负责 Moon/Planet 注册、心跳、地址发现、策略下发 |
| Moon 节点 | Rust | 中继服务器，UDP/TCP/WSS 流量转发、Session 管理 |
| 审计服务器 | Go | 接收客户端上报的日志，提供查询 API |

---

## 二、Controller API 开发

### C1. 项目初始化与框架 — 1.5 天

- [x] **C1.1** 创建 Git 仓库，建立分支模型
- [x] **C1.2** 初始化 Go 项目结构（参考 proximity-vpn-design.md §14）
  - cmd/server/main.go
  - internal/handler/ — HTTP Handler
  - internal/service/ — 业务逻辑
  - internal/model/ — 数据模型
  - internal/repository/ — 数据库访问
  - internal/middleware/ — JWT / HMAC / RBAC
  - internal/crypto/ — 加密工具
  - pkg/jwt/ / pkg/nonce/
  - migrations/ — SQL 迁移
- [x] **C1.3** 配置 Go Modules（go.mod）
- [x] **C1.4** 配置 Gin 框架（路由、中间件）
- [x] **C1.5** 配置 PostgreSQL 连接池
- [x] **C1.6** 配置 Redis（Nonce 缓存、Session）
- [x] **C1.7** 配置结构化日志（JSON，字段见开发规范 §17.1.3）
- [x] **C1.8** 配置 CI/CD（lint + test + build）
- [x] **C1.9** 统一错误码封装（符合 API 错误规范 §12.0）

---

### C2. Moon 注册与心跳 API — 1.5 天

- [x] **C2.1** `POST /api/v1/moon/register` — Moon 注册
  - [x] 验证 domain / relay_port / wss_port / udp_port
  - [x] 分配 gateway_ip（IPAM，每网络唯一）
  - [x] 生成 api_key（pk_ 前缀）和 api_secret（sk_ 前缀）
  - [x] HMAC-SHA256 注册签名验证（首次无 key，仅验证域名+IP）
  - [x] 注册成功后返回 moon_id / gateway_ip / api_key / api_secret
- [x] **C2.2** `POST /api/v1/moon/heartbeat` — Moon 心跳（每 30 秒）
  - [x] HMAC 签名认证（api_key + api_secret）
  - [x] Nonce 防重放（UUID v4，5 分钟有效）
  - [x] Timestamp 容差 ±300s
  - [x] 返回 moon_count（当前连接数）
  - [x] 返回 shutdown 指令（boolean）
- [x] **C2.3** Nonce 管理器（内存 + Redis，TTL 5 分钟）
- [x] **C2.4** API Key 轮换机制（30 天自动轮换，宽限期 24 小时）
- [x] **C2.5** Moon 心跳超时检测（90 秒无心跳标记 offline）
- [x] **C2.6** Moon 上下线事件（内部发布到 WebSocket）

---

### C3. Planet 注册与心跳 API — 1.5 天

- [x] **C3.1** `POST /api/v1/peer/register` — 设备注册（首次上线，无需 API Key）
  - [x] 验证 device_id 格式（DID_ 前缀）
  - [x] 验证设备签名（device_sig：设备私钥签名 challenge）
  - [x] 分配 assigned_ip（IPAM，设备唯一）
  - [x] 设备指纹公钥存数据库
  - [x] 返回 assigned_ip / network_info / policy_list / moon_list
- [x] **C3.2** `POST /api/v1/peer/heartbeat` — 节点心跳
  - [x] device_id 认证
  - [x] tunnel_type（UDP/TCP/WSS/MOON）
  - [x] tunnel_quality（rtt_ms / loss_rate / recv_bytes / send_bytes）
  - [x] 返回 moon_list（含域名、relay_port、wss_port、weight、online）
  - [x] 返回 policy_updates（策略变更）
- [x] **C3.3** `POST /api/v1/peer/discover` — 地址发现
  - [x] 查询目标节点的 public_ip / public_port / nat_type
  - [x] 返回 reachable（true/false）
  - [x] 仅返回信令（不转发流量）
- [x] **C3.4** Planet 设备未绑定网络时返回错误码 2001

---

### C4. IPAM（IP地址管理）— 1.5 天

- [x] **C4.1** 网络网段分配
  - [x] 默认 10.8.0.0/16，可自定义
  - [x] 分配算法：顺序分配，空闲链表
- [x] **C4.2** Moon gateway_ip 分配
  - [x] 每个 Moon 有唯一 gateway_ip
  - [x] 同一网络内 gateway_ip 不能重复
- [x] **C4.3** Planet assigned_ip 分配
  - [x] 每个设备有唯一虚拟 IP
  - [x] 同一网络内 IP 不重复
- [x] **C4.4** IP 冲突检测（创建/更新时）
- [x] **C4.5** IP 释放（设备解绑/删除网络时）
- [x] **C4.6** IP 分配历史记录

---

### C5. 流量统计 API — 0.5 天

- [x] **C5.1** `POST /api/v1/stats/traffic` — 接收 Moon/Planet 流量统计
  - [x] 支持批量上报（最大 100 条/请求）
  - [x] peer_id / session_id / direction / bytes / packets / duration_sec / tunnel_type
- [x] **C5.2** 存储到数据库（traffic_logs 表）
- [x] **C5.3** 定期聚合计算（小时/天/月）
- [x] **C5.4** 流量配额检测（超限写入 alert）

---

### C6. 客户端版本查询 API — 0.5 天

- [x] **C6.1** `GET /api/v1/version/client` — 返回最新版本信息
  - [x] latest（最新版本号）
  - [x] min_supported（最低支持版本）
  - [x] download_url（签名 URL）
  - [x] sha256（校验）
  - [x] force_update（是否强制）
- [x] **C6.2** 版本信息缓存（Redis，5 分钟过期）

---

## 三、Moon 节点开发（Rust）

### M1. 项目初始化 — 1 天

- [x] **M1.1** 创建 Rust 项目（Cargo）
- [x] **M1.2** 配置项目结构
  - src/main.rs
  - src/relay.rs — UDP/TCP/WSS 中继
  - src/signaling.rs — 信令转发
  - src/tunnel.rs — 隧道管理
  - src/session.rs — Session 生命周期
  - src/stats.rs — 流量统计上报
  - src/auth.rs — HMAC 认证
  - src/crypto.rs — ChaCha20 加密
  - src/config.rs — 配置解析
- [x] **M1.3** 命令行参数解析（clap）
  - [x] -controller：管理平台地址
  - [x] -domain：Moon 域名
  - [x] -port：relay_port（默认 11002）
  - [x] -log-level：日志级别
  - [x] -no-auto-update：禁用自动更新
  - [x] -uninstall：卸载服务
- [x] **M1.4** 配置文件读取（JSON，/etc/shanlian/moon.json）
- [x] **M1.5** 系统服务注册（systemd / Windows Service）
- [x] **M1.6** 优雅退出（SIGTERM / Ctrl-C 处理）
- [x] **M1.7** 配置 CI/CD（rust-clippy / cargo test / cargo build）

---

### M2. Moon 注册流程 — 1 天

- [x] **M2.1** 启动时读取配置文件（api_key / api_secret）
- [x] **M2.2** 向 Controller 注册
  - [x] HMAC-SHA256(secret, Nonce + Timestamp + Method + Path + Body)
  - [x] 签名头：X-Api-Key / X-Nonce / X-Timestamp / X-Signature
- [x] **M2.3** 注册失败重试（3 次，间隔 5s，指数退避）
- [x] **M2.4** 注册成功后开始监听端口
- [x] **M2.5** 心跳定时器（每 30 秒）

---

### M3. 端口监听 — 1 天

- [x] **M3.1** TCP 11002 — 流量中继 + Moon 注册/心跳（同一端口复用）
  - [x] 使用 tokio 异步网络
- [x] **M3.2** UDP 11001 — NAT 穿透辅助
- [x] **M3.3** TCP 11003 — 健康检查（HTTP，返回 JSON 状态）
- [x] **M3.4** TCP 11443 — WSS 中继（WebSocket Upgrade）
- [x] **M3.5** 端口占用检测（启动失败时报错并退出）
- [x] **M3.6** SO_REUSEPORT 优化多核性能

---

### M4. UDP 中继 — 1.5 天

- [x] **M4.1** Planet ↔ Moon UDP 隧道
- [x] **M4.2** SLNX 协议解析（Magic 0x534C4E58 / Version / Flags / Type / Session ID / Sequence #）
- [x] **M4.3** ChaCha20-Poly1305 加密/解密
- [x] **M4.4** Session ID 路由（session_id → Planet）
- [x] **M4.5** Sequence # 防重放（窗口 128）
- [x] **M4.6** CRC32C 校验

---

### M5. TCP 中继 — 1 天

- [x] **M5.1** Planet ↔ Moon TCP 隧道
- [x] **M5.2** 长连接管理（Map<peer_id, TcpStream>）
- [x] **M5.3** 数据流加密转发（分帧 + ChaCha20）
- [x] **M5.4** 心跳保活（TCP keep-alive）
- [x] **M5.5** 连接超时处理（读超时 60s）

---

### M6. WSS 中继 — 1.5 天

- [x] **M6.1** WebSocket Upgrade 处理（HTTP/1.1）
- [x] **M6.2** Sec-WebSocket-Protocol: slnx 子协议
- [x] **M6.3** WSS 帧格式解析（Channel ID 2B + Frame Type 1B + Length 2B）
- [x] **M6.4** 多路复用（65535 个 channel_id 并发）
- [x] **M6.5** Planet channel_id 分配（Controller 下发）
- [x] **M6.6** TLS 1.3 握手（rustls）

---

### M7. SLNX 隧道协议实现 — 2 天

- [x] **M7.1** 隧道包封装
  - [x] Magic: 0x534C4E58
  - [x] Version: 1
  - [x] Flags: 0x01=握手 / 0x02=数据 / 0x04=心跳 / 0x08=关闭
  - [x] Type: 0x01=IP包 / 0x02=隧道控制 / 0x03=FEC
  - [x] Session ID: 8 字节
  - [x] Sequence #: 4 字节
  - [x] Payload Length: 2 字节
  - [x] Payload Checksum: CRC32C
- [x] **M7.2** ChaCha20-Poly1305 加密（Rust 使用 ring 或 chacha20poly1305 crate）
- [x] **M7.3** Session Key 派生（HKDF）
- [x] **M7.4** 防重放（Sequence # 窗口）
- [x] **M7.5** 握手流程（SYN / SYN-ACK / ACK）

---

### M8. 信令转发 — 1.5 天

- [x] **M8.1** P2P 握手信令
  - [x] 接收 Planet A 的握手请求
  - [x] 查询 Planet B 的当前 Moon 地址
  - [x] 转发握手消息到 Planet B
- [x] **M8.2** 中转加密密钥协商（ECDH X25519）
- [x] **M8.3** NAT 类型检测辅助（UDP 打洞支持）

---

### M9. Moon-Moon 隧道 — 1.5 天

- [x] **M9.1** Moon A 向 Controller 查询 Moon B 的 domain 和 public_ip
- [x] **M9.2** Moon A 连接到 Moon B:11443（WSS/TLS 1.3）
- [x] **M9.3** 双向 HMAC 认证（各自 api_key / api_secret）
- [x] **M9.4** 建立 ChaCha20 加密隧道（共享 session key）
- [x] **M9.5** 多路复用（多个 Planet 对共享同一 Moon-Moon 连接）
- [x] **M9.6** Controller 通知 Planet B：需要通过 Moon B 中继

---

### M10. Session 管理 — 1 天

- [x] **M10.1** Session 创建（Planet 连接时）
  - [x] Session ID 唯一（UUID v4）
  - [x] 记录 Planet ID / Moon ID / Tunnel Type / Established At
- [x] **M10.2** Session 销毁（Planet 断开时）
- [x] **M10.3** 心跳超时（90 秒无心跳，Session 标记为 stale）
- [x] **M10.4** Session 统计上报 Controller（每 60 秒）
- [x] **M10.5** Moon 重启后 Session 不恢复（由 Planet 切换到备用 Moon）

---

### M11. 流量统计 — 0.5 天

- [x] **M11.1** 实时流量统计
  - [x] 按 Planet 统计
  - [x] 按方向（in / out）
  - [x] 按 Tunnel Type
- [x] **M11.2** 定期上报 Controller（POST /api/v1/stats/traffic）
- [x] **M11.3** 本地聚合缓存（减少上报频率）

---

### M12. 健康检查 — 0.5 天

- [x] **M12.1** TCP 11003 HTTP 健康检查端点
- [x] **M12.2** 返回 JSON 状态
  ```json
  {
    "status": "healthy",
    "moon_id": "uuid",
    "uptime": 3600,
    "connections": 150,
    "version": "0.0.1"
  }
  ```
- [x] **M12.3** Prometheus metrics 端点（/metrics）

---

### M13. FEC 前向纠错 — 1 天

- [x] **M13.1** Reed-Solomon 编解码（k=10, n=14）
- [x] **M13.2** 丢包率检测（统计最近 1000 个包）
- [x] **M13.3** 自动启用（丢包率 > 5%）
- [x] **M13.4** FEC 组包（10 原始包 + 4 冗余包）

---

### M14. 自动更新 — 1 天

- [x] **M14.1** 后台定时检测 Controller 版本（每 24 小时）
- [x] **M14.2** 下载新版本到临时目录
- [x] **M14.3** RSA-SHA256 签名校验
- [x] **M14.4** 优雅替换（二进制文件锁）
- [x] **M14.5** 更新完成后自动重启并重新注册

---

## 四、审计服务器开发

### A1. 项目初始化 — 0.5 天

- [x] **A1.1** 创建 Go 项目（audit/ 目录）
- [x] **A1.2** 项目结构
  - cmd/server/main.go
  - internal/receiver.go — 日志接收（HTTPS）
  - internal/storage.go — 存储（SQLite 开发 / ClickHouse 生产）
  - internal/alert.go — 告警引擎
  - internal/forward.go — 转发到管理平台
- [x] **A1.3** 配置 Gin + TLS（HTTPS）

---

### A2. 日志接收 API — 1.5 天

- [x] **A2.1** `POST /api/v1/audit/log` — 接收 Planet 日志
  - [x] Bearer Token 认证（audit_api_key）
  - [x] mTLS 双向认证（可选）
- [x] **A2.2** 日志类型处理
  - [x] access — URL 访问日志
  - [x] traffic — 流量日志
  - [x] policy_change — 策略变更日志
  - [x] tunnel — 隧道日志
  - [x] online — 上下线日志
- [x] **A2.3** 批量接收（最大 100 条/请求）
- [x] **A2.4** 内存队列缓冲（最大 1000 条）
- [x] **A2.5** 异步写入存储

---

### A3. 日志存储 — 1 天

- [x] **A3.1** 开发阶段：SQLite
- [x] **A3.2** 生产阶段：ClickHouse（高吞吐写入）
- [x] **A3.3** 按时间分区（按天）
- [x] **A3.4** 索引（peer_id / network_id / timestamp）
- [x] **A3.5** 数据压缩（LZ4）

---

### A4. 告警引擎 — 1 天

- [x] **A4.1** 告警规则
  - [x] Moon 离线超过阈值（默认 3 分钟）
  - [x] 带宽超限（> 配额 80%）
  - [x] 节点异常行为（短时间内频繁上下线）
- [x] **A4.2** 告警发送
  - [x] 邮件（SMTP）
  - [x] 钉钉 Webhook
  - [x] 企业微信 Webhook
  - [x] 飞书 Webhook
- [x] **A4.3** 告警去重（同一告警 5 分钟内不重复发送）
- [x] **A4.4** 告警发送失败自动重试（3 次，指数退避）

---

### A5. 日志查询 API — 0.5 天

- [x] **A5.1** `GET /api/v1/audit/logs` — 查询日志
  - [x] 筛选（network_id / peer_id / type / time_range）
  - [x] 分页
- [x] **A5.2** `GET /api/v1/audit/stats` — 统计聚合

---

## 五、测试计划

### T1. 单元测试 — 持续

- [x] **T1.1** Controller：每个 Handler 测试（覆盖率 ≥ 70%）
- [x] **T1.2** Moon：每个模块测试（覆盖率 ≥ 70%）
- [x] **T1.3** 审计服务器：每个 Handler 测试

### T2. 集成测试 — 3 天

- [x] **T2.1** Moon ↔ Controller 注册流程
- [x] **T2.2** Planet ↔ Moon 隧道建立（UDP / TCP / WSS）
- [x] **T2.3** Planet ↔ Planet P2P 直连（NAT 打洞）
- [x] **T2.4** Moon-Moon 中继
- [x] **T2.5** 策略下发生效
- [x] **T2.6** 日志上报完整链路

### T3. 压力测试 — 1 天

- [x] **T3.1** 单 Moon 支持 1000 并发连接
- [x] **T3.2** 单 Controller 支持 5000 并发注册
- [x] **T3.3** 审计服务器支持 10,000 qps 写入

### T4. 故障测试 — 1 天

- [x] **T4.1** Moon 宕机，Planet 自动切换
- [x] **T4.2** Controller 宕机，Moon 重连
- [x] **T4.3** 网络中断恢复
- [x] **T4.4** Session 不恢复验证

---

## 六、进度里程碑

```
第 1-2 天：  C1（C 项目初始化 + 框架）
第 3-5 天：  C2 + C3（Moon/Planet 注册心跳）
第 6-7 天：  C4 + C5 + C6（IPAM + 统计 + 版本）
第 8-9 天：  M1 + M2（M 项目初始化 + 注册）
第 10-12 天：M3 + M4 + M5（端口监听 + UDP/TCP 中继）
第 13-15 天：M6 + M7 + M8（WSS 中继 + SLNX 协议 + 信令）
第 16-18 天：M9 + M10 + M11（Moon-Moon + Session + 统计）
第 19 天：   M12 + M13（健康检查 + FEC）
第 20 天：   M14（自动更新）
第 21 天：   A1 + A2（审计服务器初始化 + 日志接收）
第 22 天：   A3 + A4 + A5（存储 + 告警 + 查询）
第 23-26 天：T1（单元测试）
第 27-29 天：T2（集成测试）
第 30 天：   T3（压力测试）
第 31-32 天：T4（故障测试）

预计总工期：32 天
```

---

*文档版本：v0.0.0 | 更新：2026-04-15*
