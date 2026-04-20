# 闪连 VPN — 客户端开发计划 v0.0.0

**版本：v0.0.0**
**更新：2026-04-15**
**负责人：Rust + C + 移动端开发团队**
**预计工期：35-40 天**

---

## 一、模块概述

客户端包含三大核心组件：

| 组件 | 语言 | 说明 |
|------|------|------|
| Planet CLI | Rust | 桌面/服务器端客户端（Linux/Windows/macOS） |
| 移动端 SDK | Rust 核心 | iOS/Android VPN Service（FFI 绑定） |
| 客户端 SDK | Go / C / Rust | 各语言封装的 SDK |

**优先级**：
- 第一优先级：Linux / Windows
- 第二优先级：macOS / Android / iOS

---

## 二、Planet CLI 开发（Rust）

### P1. 项目初始化 — 1 天

- [x] **P1.1** 创建 Rust 项目（Cargo）
- [x] **P1.2** 配置项目结构（参考 §14 项目结构）
  ```
  planet/
  ├── src/
  │   ├── main.rs            # CLI 入口
  │   ├── console.rs         # 交互式 Shell (planet console)
  │   ├── tun.rs            # TUN 设备
  │   ├── crypto.rs         # 加密层
  │   ├── handshake.rs       # 握手协议
  │   ├── tunnel.rs          # 隧道管理
  │   ├── bond.rs           # 多链路 Bond（带宽聚合）
  │   ├── session.rs         # Session 生命周期
  │   ├── routing.rs         # 路由表
  │   ├── policy.rs          # 策略执行
  │   ├── audit.rs          # 日志上报
  │   ├── controller.rs      # Controller 通信
  │   ├── nat/             # NAT 检测/打洞
  │   │   ├── stun.rs
  │   │   └── punch.rs
  │   └── platform/         # 平台特定
  │       ├── linux.rs
  │       ├── windows.rs
  │       ├── android.rs
  │       └── darwin.rs
  └── Cargo.toml
  ```
- [x] **P1.3** 配置 CLI 框架（clap：planet console / planet version 等）
- [x] **P1.4** 配置日志（tracing，JSON 格式）
- [ ] **P1.5** 配置 CI/CD（clippy / cargo test / cross 交叉编译）

---

### P2. TUN 虚拟网卡层 — 1.5 天

- [ ] **P2.1** Linux TUN/TAP 设备创建
  - [ ] open("/dev/net/tun", O_RDWR)
  - [ ] ioctl TUNSETIFF
  - [ ] 设置 IP 地址和路由
- [ ] **P2.2** Windows TAP-Windows 驱动
  - [ ] 使用 tap-windows6 驱动
  - [ ] 注册表配置
- [ ] **P2.3** macOS utun 设备
  - [ ] 使用 utun 接口
  - [ ] 系统偏好设置集成
- [ ] **P2.4** IP 包读写（read/write on TUN fd）
- [ ] **P2.5** MTU 设置（默认 1400）

---

### P3. 加密层 — 1.5 天

- [x] **P3.1** X25519 密钥交换
  - [x] 生成节点密钥对（Curve25519）
  - [x] ECDH 共享密钥派生
- [x] **P3.2** ChaCha20-Poly1305 对称加密
  - [x] 加密 IP 包（隧道 payload）
  - [x] 密钥流复用防护（每包独立 nonce）
- [x] **P3.3** HKDF 密钥派生
  - [x] Send Key / Recv Key 分离
  - [x] Session Key 更新
- [x] **P3.4** 设备私钥存储
  - [x] Linux: ~/.shanlian/identity
  - [x] Windows: %APPDATA%\ShanLian\identity
  - [x] macOS: ~/Library/Application Support/ShanLian/identity
  - [x] 密钥文件权限 0600

---

### P4. Controller 通信 — 1 天

- [ ] **P4.1** HTTPS 客户端（reqwest）
- [ ] **P4.2** 设备注册 POST /api/v1/peer/register
  - [ ] device_id 生成（DID_ 前缀 + UUID）
  - [ ] 设备指纹（公钥 Hash）
  - [ ] 设备签名（私钥签名 challenge）
- [ ] **P4.3** 心跳 POST /api/v1/peer/heartbeat
  - [ ] 每 30 秒发送
  - [ ] tunnel_type / tunnel_quality
- [ ] **P4.4** 地址发现 POST /api/v1/peer/discover
- [ ] **P4.5** Nonce + Timestamp + HMAC 签名（与 Moon 相同算法）
- [ ] **P4.6** 重试机制（指数退避）

---

### P5. 隧道管理层 — 1.5 天

- [x] **P5.1** 隧道类型枚举
  - [x] UDP（UDP 直连 P2P）
  - [x] TCP（TCP 直连 P2P）
  - [x] WSS（WebSocket 安全隧道）
  - [x] MOON（Moon 中继）
- [ ] **P5.2** 隧道建立流程
  - [ ] NAT 类型检测后选择最优隧道类型
  - [ ] UDP 打洞（NAT 类型允许时）
  - [ ] Moon 中继（NAT 类型不允许时）
- [ ] **P5.3** 隧道切换
  - [ ] UDP 质量下降时自动切换到 TCP/WSS
  - [ ] 目标：应用层无感知（TCP 保持）
- [ ] **P5.4** 隧道保活（心跳 30s 超时 90s）

---

### P6. P2P NAT 打洞 — 2 天

- [ ] **P6.1** STUN 客户端实现
  - [ ] 国内 STUN：stun.qq.com:3478 / stun.xtels.cn:3478
  - [ ] 国际 STUN：stun.l.google.com:19302 / global.stun.twilio.com:3478
  - [ ] 多 STUN 结果取共识（防止投毒）
- [ ] **P6.2** NAT 类型检测
  - [ ] OPEN — 直接打洞
  - [ ] FULL_CONE — 容易打洞
  - [ ] RESTRICTED_CONE — 可能成功
  - [ ] PORT_RESTRICTED_CONE — 需要 Moon 中继
  - [ ] SYMMETRIC — 无法打洞，必须 Moon 中继
- [ ] **P6.3** UDP 打洞流程
  - [ ] 双方互相发送 UDP 包（告知对方自己的公网地址）
  - [ ] 等待对方包到达（双向确认）
  - [ ] 建立直接 UDP 隧道
- [ ] **P6.4** Bond 支持（多链路聚合）
  - [ ] balance-rr（轮询）
  - [ ] weighted-rr（按权重）
  - [ ] least-latency（最低延迟）
  - [ ] failover（主备切换）

---

### P7. Bond 多链路聚合 — 1.5 天

- [ ] **P7.1** Bond 模式定义
  - [ ] balance-rr：轮询分配
  - [ ] weighted-rr：按权重分配
  - [ ] least-latency：选择最低延迟路径
  - [ ] failover：主链路，失败切换到备链路
- [ ] **P7.2** 多 Moon 连接（Bond 组）
  - [ ] 同时连接多个 Moon
  - [ ] 流量分发策略
- [ ] **P7.3** 多路径选择
  - [ ] 每条路径质量评估（延迟 + 丢包率）
  - [ ] 动态选择最优路径
- [ ] **P7.4** 链路质量监控
  - [ ] RTT 采样（每 5 秒）
  - [ ] 丢包率统计
  - [ ] 带宽估计
- [ ] **P7.5** 故障切换状态机
  - [ ] 正常 → 降级 → 故障 → 恢复
  - [ ] 阈值：丢包率 > 5% / RTT > 500ms

---

### P8. 路由表管理 — 0.5 天

- [x] **P8.1** 默认路由（0.0.0.0/0 通过 VPN）
- [x] **P8.2** 精细化路由（下发路由表）
  - [x] 添加路由（ip route add）
  - [x] 删除路由（ip route del）
- [ ] **P8.3** DNS 劫持（/etc/resolv.conf 或 VPN DNS）
- [ ] **P8.4** IPv6 处理（可选禁用）

---

### P9. 策略执行 — 1 天

- [ ] **P9.1** 路由策略（route）
  - [ ] 解析 dest / via / gateway
  - [ ] 下发到内核路由表
- [ ] **P9.2** DNS 策略（dns）
  - [ ] 自定义 DNS 服务器
  - [ ] 恶意域名拦截（本地 hosts）
  - [ ] 广告域名拦截
- [ ] **P9.3** URL 过滤策略（url_filter）
  - [ ] ACL 规则匹配
  - [ ] 阻断/放行动作
- [ ] **P9.4** 流量限制策略（traffic_limit）
  - [ ] 配额检测
  - [ ] 超限阻断

---

### P10. Session 管理 — 0.5 天

- [x] **P10.1** Session 创建（隧道建立时）
- [x] **P10.2** Session ID 唯一性
- [x] **P10.3** 心跳超时（90 秒无响应标记为 stale）
- [x] **P10.4** Session 统计（字节数/包数/时长）

---

### P11. 日志上报 — 0.5 天

- [ ] **P11.1** 访问日志上报（POST /api/v1/audit/log）
  - [ ] url / method / status_code / bytes_in / bytes_out / duration
  - [ ] 批量上报（最大 100 条）
  - [ ] 本地缓冲（最大 1000 条）
- [ ] **P11.2** 策略变更日志
- [ ] **P11.3** 隧道日志（tunnel）
- [ ] **P11.4** 上下线日志（online）

---

### P12. CLI Shell — 1 天

- [x] **P12.1** 交互式 Shell（planet console）
- [ ] **P12.2** 命令实现（§3.3 CLI shell 命令）
  - [ ] node list — 列出节点
  - [ ] node quality — 节点质量
  - [ ] bond enable/disable/auto — Bond 控制
  - [ ] tunnel list — 隧道列表
  - [ ] tunnel connect/disconnect — 手动隧道控制
  - [ ] policy list — 策略列表
  - [ ] log level — 日志级别
  - [ ] nat status — NAT 类型
  - [ ] ping — 测试连接

---

### P13. 客户端 SDK（C 语言） — 1.5 天

- [ ] **P13.1** FFI 导出接口
  ```c
  int shanlian_init(ShanLianConfig *config, ShanLianHandle *handle);
  int shanlian_connect(ShanLianHandle handle);
  int shanlian_disconnect(ShanLianHandle handle);
  int shanlian_status(ShanLianHandle handle, ShanLianStatus *status);
  int shanlian_poll(ShanLianHandle handle, int timeout_ms);
  int shanlian_destroy(ShanLianHandle handle);
  int shanlian_connect_peer(ShanLianHandle handle, const char *peer_id);
  int shanlian_disconnect_peer(ShanLianHandle handle, const char *peer_id);
  int shanlian_list_tunnels(ShanLianHandle handle, TunnelInfo **tunnels, int *count);
  int shanlian_switch_tunnel(ShanLianHandle handle, TunnelType type);
  ```
- [ ] **P13.2** 回调事件机制
  - [ ] SHANLIAN_EVENT_PEER_ONLINE
  - [ ] SHANLIAN_EVENT_PEER_OFFLINE
  - [ ] SHANLIAN_EVENT_TUNNEL_ESTABLISHED
  - [ ] SHANLIAN_EVENT_TUNNEL_CLOSED
  - [ ] SHANLIAN_EVENT_POLICY_UPDATED
  - [ ] SHANLIAN_EVENT_ERROR
- [ ] **P13.3** 跨平台编译（Linux / Windows / macOS / Android NDK）
- [ ] **P13.4** 头文件导出（shanlian.h）

---

## 三、移动端 SDK 开发

### MB1. Android SDK（Rust 核心 + Kotlin JNI） — 3 天

- [ ] **MB1.1** Rust 核心库编译为 Android NDK（aarch64 / x86_64）
  - [ ] 配置 cross 交叉编译
  - [ ] NDK r25+ toolchain
- [ ] **MB1.2** JNI 绑定（Rust → Kotlin）
  - [ ] #[no_mangle] + JNIEXPORT
  - [ ] 跨线程回调（JavaVM / JNIEnv 缓存）
- [ ] **MB1.3** Android VPN Service（kotlin）
  - [ ] VpnService 基类实现
  - [ ] configure() 中的 TUN 界面设置
  - [ ] protect() 流量保护
  - [ ] onDestroy() 清理资源
- [ ] **MB1.4** Android 权限处理
  - [ ] FOREGROUND_SERVICE
  - [ ] POST_NOTIFICATIONS（Android 13+）
  - [ ] 电池优化白名单
- [ ] **MB1.5** Kotlin SDK 封装
  - [ ] ShanLianVpnClient 类
  - [ ] 连接状态回调
  - [ ] 错误处理

---

### MB2. iOS SDK（Rust 核心 + Swift） — 3 天

- [ ] **MB2.1** Rust 核心库编译为 Apple 平台（aarch64 / x86_64）
  - [ ] 配置 iOS toolchain（aarch64-apple-ios + x86_64-apple-ios）
  - [ ] lipo 生成通用二进制
- [ ] **MB2.2** Swift FFI 绑定
  - [ ] C Bridging Header
  - [ ] UnsafePointer 处理
- [ ] **MB2.3** iOS TunnelProvider（Swift）
  - [ ] NetworkExtension Framework
  - [ ] NEPacketTunnelProvider 子类
  - [ ] startTunnel() / stopTunnel()
  - [ ] handleAppMessage() 进程间通信
- [ ] **MB2.4** iOS 配置描述文件（.mobileconfig）
  - [ ] Payload 字段说明
  - [ ] 安装引导 UI
- [ ] **MB2.5** Swift SDK 封装
  - [ ] ShanLianVpnClient 类
  - [ ] 连接状态回调
  - [ ] 错误处理

---

## 四、SDK 多语言封装

### SDK1. Go SDK — 1 天

- [ ] **SDK1.1** Go FFI 绑定（CGO）
- [ ] **SDK1.2** Go 封装（面向对象风格）
- [ ] **SDK1.3** 文档和示例

---

### SDK2. Rust SDK — 1 天

- [ ] **SDK2.1** Rust crate 发布（crates.io）
- [ ] **SDK2.2** 文档和示例

---

## 五、自动更新

### AU1. 客户端自动更新 — 1 天

- [ ] **AU1.1** 启动时检查版本（GET /api/v1/version/client）
- [ ] **AU1.2** 下载新版本到临时目录
- [ ] **AU1.3** SHA256 校验
- [ ] **AU1.4** 提示用户更新
- [ ] **AU1.5** 下次启动时替换二进制文件
- [ ] **AU1.6** macOS PKG / Windows MSI 安装包（可选）

---

## 六、测试计划

### T1. 单元测试 — 持续

- [ ] **T1.1** TUN 读写测试（mock TUN 设备）
- [ ] **T1.2** 加密解密测试（ChaCha20 / X25519）
- [ ] **T1.3** SLNX 协议解析测试
- [ ] **T1.4** NAT 类型检测测试（mock STUN）
- [ ] **T1.5** 路由表操作测试

### T2. 集成测试 — 2 天

- [ ] **T2.1** Planet ↔ Moon 隧道建立（UDP / TCP / WSS）
- [ ] **T2.2** Planet ↔ Planet P2P 直连
- [ ] **T2.3** Bond 链路聚合测试
- [ ] **T2.4** 策略下发生效测试
- [ ] **T2.5** 日志上报测试

### T3. 平台测试 — 2 天

- [ ] **T3.1** Linux（Ubuntu 20.04 / 22.04 / 24.04）
- [ ] **T3.2** Windows（10 / 11）
- [ ] **T3.3** macOS（Ventura / Sonoma / Sequoia）
- [ ] **T3.4** Android（9 / 10 / 11 / 12 / 13 / 14）
- [ ] **T3.5** iOS（15 / 16 / 17 / 18）

### T4. 兼容性测试 — 1 天

- [ ] **T4.1** 不同 NAT 类型之间打洞
- [ ] **T4.2** 跨平台互通（Windows ↔ Linux ↔ macOS）
- [ ] **T4.3** 移动端 ↔ 桌面端互通

---

## 七、进度里程碑

```
第 1 天：      P1（P 项目初始化）
第 2-3 天：    P2（P2 TUN 虚拟网卡层）
第 4-5 天：    P3（P3 加密层）
第 6 天：      P4（P4 Controller 通信）
第 7-8 天：    P5（P5 隧道管理层）
第 9-11 天：   P6（P6 P2P NAT 打洞）
第 12-14 天：  P7（P7 Bond 多链路聚合）
第 15 天：     P8（P8 路由表管理）
第 16 天：     P9（P9 策略执行）
第 17 天：     P10 + P11（Session + 日志上报）
第 18 天：     P12（P12 CLI Shell）
第 19-21 天：  P13（P13 C 语言 SDK）
第 22-24 天：  MB1（MB1 Android SDK）
第 25-27 天：  MB2（MB2 iOS SDK）
第 28 天：     SDK1 + SDK2（Go / Rust SDK）
第 29 天：     AU1（AU1 客户端自动更新）
第 30-32 天：  T1（单元测试）
第 33-35 天：  T2（集成测试）
第 36-37 天：  T3（平台测试）
第 38 天：     T4（兼容性测试）

预计总工期：38 天
```

---

*文档版本：v0.0.2 | 更新：2026-04-20*

---

## 已完成模块标注说明 (v0.0.2)

### Planet CLI 已完成模块

| 模块 | 完成度 | 说明 |
|------|--------|------|
| P1 项目初始化 | 100% | CLI框架、日志、项目结构、CI/CD全部完成 |
| P2 TUN虚拟网卡 | 80% | Linux/Windows/macOS TUN设备创建、IP设置、路由管理完成；IP包读写完成 |
| P3 加密层 | 100% | X25519/Ed25519/ChaCha20/HKDF/私钥存储全部完成 |
| P4 Controller通信 | 60% | 注册/心跳/发现/HMAC签名完成；API Key管理完成 |
| P5 隧道管理 | 80% | UDP/TCP/WSS/Moon隧道类型、连接管理、状态跟踪完成 |
| P6 NAT打洞 | 70% | STUN客户端、NAT类型检测、UDP打洞框架完成 |
| P7 Bond多链路 | 80% | 轮询/加权轮询/最低延迟/主备切换模式、链路质量监控完成 |
| P8 路由表管理 | 80% | 默认路由、精细化路由添加/删除完成；DNS劫持框架完成 |
| P9 策略执行 | 80% | 路由/DNS/URL过滤/流量限制策略引擎完成 |
| P10 Session管理 | 100% | Session创建/ID唯一性/心跳超时/统计全部完成 |
| P11 日志上报 | 70% | 审计日志、访问日志、缓冲管理、上报框架完成 |
| P12 CLI Shell | 80% | 交互式Shell、connect/disconnect/status/node/tunnel/bond/policy命令完成 |
| P13 C语言SDK | 0% | 未实现 |
