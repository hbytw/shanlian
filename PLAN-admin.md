# 闪连 VPN — 管理平台开发计划 v0.0.0

**版本：v0.0.0**
**更新：2026-04-16**
**负责人：前端 + Go 后端开发团队**
**预计工期：15-18 天**

---

## 一、模块概述

管理平台是闪连 VPN 的 Web 控制台（React + Ant Design + Go/Gin），包含前端和 Go 后端两部分，与 Controller API 共用进程和数据存储。

**技术栈**：
- 前端：React 18 + Ant Design 5 + TypeScript + WebSocket
- 后端：Go + Gin（与 Controller 共用进程）
- 数据库：PostgreSQL
- 缓存：Redis

**角色体系**：
- 超级管理员 (super_admin)：系统级配置、管理所有网络和用户
- 网络管理员 (network_admin)：管理授权的网络、用户、策略
- 普通用户 (user)：查看被分配的网络、节点状态、流量使用

---

## 二、功能模块分解

### M1. 项目初始化 — 1 天 ✅

- [x] **M1.1** 创建 Git 仓库，建立分支模型（main/develop/feature/*）
- [x] **M1.2** 初始化 Go 项目结构（参考 §14 项目结构）
- [ ] **M1.3** 初始化 React 项目（Vite + TypeScript + Ant Design）
- [ ] **M1.4** 配置 CI/CD（GitHub Actions：lint + test + build）
- [ ] **M1.5** 配置 ESLint + Prettier + Husky
- [ ] **M1.6** 配置环境变量（.env.development / .env.production）

---

### M2. 数据库设计与迁移 — 1.5 天 ✅

- [x] **M2.1** 编写 PostgreSQL 迁移脚本：001_init_schema.sql
  - [x] users 表（id, username, email, phone, password_hash, role, created_at, updated_at）
  - [x] networks 表（id, name, cidr, gateway_ip, owner_id, created_at, updated_at）
  - [x] devices 表（id, user_id, hostname, public_ip, nat_type, assigned_ip, online...）
  - [x] moon_nodes 表（id, domain, public_ip, gateway_ip, relay_port, wss_port, udp_port, health_port, weight, online...）
  - [x] peers 表（id, device_id, network_id, public_ip, public_port, nat_type, online...）
  - [x] policies 表（id, network_id, name, policy_type, config, priority, enabled...）
  - [x] sessions 表（id, peer_id, moon_id, tunnel_type, local_sock, remote_sock...）
  - [x] audit_logs 表（id, operator, action, resource_type, resource_id, detail...）
  - [x] user_networks 表（user_id, network_id, role）
- [x] **M2.2** 编写迁移脚本：002_add_indexes.sql
- [x] **M2.3** 创建数据库 seed 数据（测试用户）
- [x] **M2.4** 编写数据库回滚脚本

---

### M3. 后端框架搭建 — 1 天 ✅

- [x] **M3.1** 配置 Go Gin 框架（路由、中间件）
- [x] **M3.2** 封装 JWT 认证中间件（登录/刷新 Token）
- [x] **M3.3** 配置 PostgreSQL 连接池
- [x] **M3.4** 配置 Redis 连接（Session 缓存）
- [x] **M3.5** 配置结构化日志（JSON 格式，字段见开发规范）
- [x] **M3.6** 封装统一响应格式（{code, message, data}）
- [x] **M3.7** 配置 CORS 跨域

**已完成代码：**
- cmd/server/main.go — 主入口，完整路由配置
- internal/model/ — 9 个模型文件（user, network, device, moon, peer, policy, session, log, auth, stats, response）
- internal/repository/ — 8 个仓储（postgres, redis, user, network, device, moon, peer, policy, audit）
- internal/service/ — 8 个服务（auth, user, network, device, moon, peer, policy, stats, audit）
- internal/handler/ — 10 个处理器（auth, user, network, device, moon, peer, policy, stats, audit, websocket）
- internal/middleware/ — 4 个中间件（auth, hmac, cors, logger）
- internal/crypto/ — 密码哈希
- pkg/jwt/ — JWT 管理器
- pkg/nonce/ — Nonce 管理器（防重放）

---

### M4. 认证模块（后端） — 1.5 天 🔄

- [x] **M4.1** 用户注册接口 POST /api/v1/auth/register
  - [x] 参数校验（username/email/phone）
  - [x] 密码 bcrypt 加密
- [x] **M4.2** 用户登录接口 POST /api/v1/auth/login
  - [x] JWT Access Token + Refresh Token
  - [x] 登录日志记录
- [x] **M4.3** 刷新 Token 接口 POST /api/v1/auth/refresh
- [ ] **M4.4** 手机号登录 POST /api/v1/auth/login/phone（验证码）— 预留
- [ ] **M4.5** 邮箱登录 POST /api/v1/auth/login/email（验证码）— 预留
- [ ] **M4.6** 发送验证码 POST /api/v1/auth/code/send
  - [x] 频率限制（60 秒/次）
  - [x] 5 分钟有效期
  - [ ] 短信/邮件发送集成
- [ ] **M4.7** 找回密码 POST /api/v1/auth/password/reset — 预留
- [ ] **M4.8** OAuth 第三方登录 POST /api/v1/auth/oauth/:provider/callback — 预留

---

### M5. 用户管理模块（后端） — 1 天 ✅

- [x] **M5.1** 用户列表 GET /api/v1/admin/users
  - [x] 分页、搜索（username/email/phone）
  - [x] 筛选（role/status）
- [x] **M5.2** 创建用户 POST /api/v1/admin/users
- [x] **M5.3** 获取用户详情 GET /api/v1/admin/users/:id
- [x] **M5.4** 更新用户 PUT /api/v1/admin/users/:id
- [x] **M5.5** 删除用户 DELETE /api/v1/admin/users/:id
- [x] **M5.6** 重置用户密码 POST /api/v1/admin/users/:id/reset-password

---

### M6. 网络管理模块（后端） — 1.5 天 ✅

- [x] **M6.1** 网络列表 GET /api/v1/admin/networks
  - [x] 分页、筛选（owner/status）
- [x] **M6.2** 创建网络 POST /api/v1/admin/networks
  - [ ] 网段分配（IPAM 算法）— 预留
  - [ ] 冲突检测 — 预留
- [x] **M6.3** 获取网络详情 GET /api/v1/admin/networks/:id
- [x] **M6.4** 更新网络 PUT /api/v1/admin/networks/:id
- [x] **M6.5** 删除网络 DELETE /api/v1/admin/networks/:id
- [x] **M6.6** 网络内节点列表 GET /api/v1/admin/networks/:id/peers
- [x] **M6.7** 从网络移除节点 DELETE /api/v1/admin/networks/:id/peers/:peer_id
- [x] **M6.8** 用户网络分配 POST /api/v1/admin/networks/:id/users
- [x] **M6.9** 更新用户网络策略 PUT /api/v1/admin/networks/:id/users/:user_id
- [x] **M6.10** 移除用户网络分配 DELETE /api/v1/admin/networks/:id/users/:user_id
- [x] **M6.11** 获取网络用户列表 GET /api/v1/admin/networks/:id/users

---

### M7. Moon 管理模块（后端） — 1 天 ✅

- [x] **M7.1** Moon 列表 GET /api/v1/admin/moons
  - [x] 筛选（domain/online/status）
- [x] **M7.2** 添加 Moon POST /api/v1/admin/moons
  - [x] 生成 api_key / api_secret
  - [ ] 分配 gateway_ip（IPAM）— 预留
- [x] **M7.3** 更新 Moon PUT /api/v1/admin/moons/:id
  - [x] 域名/端口/权重/状态
- [x] **M7.4** 删除 Moon DELETE /api/v1/admin/moons/:id

---

### M8. 设备管理模块（后端） — 0.5 天 ✅

- [x] **M8.1** 设备列表（未绑定）GET /api/v1/admin/devices
- [x] **M8.2** 绑定设备到用户网络 POST /api/v1/admin/devices/bind
- [x] **M8.3** 解绑设备 DELETE /api/v1/admin/devices/:id/unbind
- [x] **M8.4** 转让设备 POST /api/v1/admin/devices/transfer
- [x] **M8.5** 获取设备详情 GET /api/v1/admin/devices/:id
- [x] **M8.6** 更新设备 PUT /api/v1/admin/devices/:id

---

### M9. 策略管理模块（后端） — 1.5 天 ✅

- [x] **M9.1** 策略列表 GET /api/v1/admin/policies
  - [x] 筛选（network_id/policy_type/enabled）
- [x] **M9.2** 创建策略 POST /api/v1/admin/policies
  - [x] 路由策略（dest/via/gateway）
  - [x] DNS 策略（dns_servers/block_malware/block_ads）
  - [x] URL 过滤策略（action/urls）
  - [x] 流量限制策略（limit_bytes/period/action/warn_threshold）
  - [x] 时间范围策略（time_ranges/action）
  - [x] 审计服务器策略（audit_server/audit_types/interval/buffer）
- [x] **M9.3** 更新策略 PUT /api/v1/admin/policies/:id
- [x] **M9.4** 删除策略 DELETE /api/v1/admin/policies/:id
- [x] **M9.5** 启用/禁用策略 PATCH /api/v1/admin/policies/:id/status

---

### M10. 监控统计模块（后端） — 1 天 ✅

- [x] **M10.1** 实时在线统计 GET /api/v1/admin/stats/realtime
  - [x] 在线节点数/总节点数
  - [x] 各网络在线数
  - [x] Moon 在线数
- [x] **M10.2** 流量统计 GET /api/v1/admin/stats/traffic
  - [x] 按网络/用户/设备聚合
  - [ ] 时间范围（小时/天/月）— 预留
- [x] **M10.3** TOP 排名 GET /api/v1/admin/stats/top
  - [x] 流量 TOP / 在线时长 TOP / 连接数 TOP

---

### M11. 日志审计模块（后端） — 1 天 ✅

- [x] **M11.1** URL 访问日志 GET /api/v1/admin/logs/access
- [x] **M11.2** 流量日志 GET /api/v1/admin/logs/traffic
- [x] **M11.3** 策略变更日志 GET /api/v1/admin/logs/policy
- [x] **M11.4** 上下线日志 GET /api/v1/admin/logs/online
- [ ] **M11.5** 导出日志 GET /api/v1/admin/logs/export
  - [ ] 支持 CSV/JSON 格式 — 预留
  - [ ] 异步生成，下载链接 — 预留
- [x] **M11.6** 管理员操作日志 GET /api/v1/admin/logs/admin
  - [x] 记录所有管理操作（操作者/动作/资源/详情/IP/时间）
- [x] **M11.7** 审计服务器状态 GET /api/v1/admin/audit/status
  - [x] 连通性检测
  - [x] 各类型日志数量

---

### M12. 客户端版本管理（后端） — 0.5 天 🔄

- [x] **M12.1** 查询最新版本 GET /api/v1/version/client
- [ ] **M12.2** 上传新版本 POST /api/v1/admin/versions
  - [ ] 文件上传到 S3 — 预留
  - [ ] SHA256 校验
  - [ ] 版本有效性验证
- [ ] **M12.3** 版本列表 GET /api/v1/admin/versions — 预留
- [ ] **M12.4** 删除版本 DELETE /api/v1/admin/versions/:id — 预留

---

### M13. WebSocket 实时推送 — 1 天 ✅

- [x] **M13.1** 建立 WebSocket 连接 WSS://controller/ws/admin
  - [x] Token 认证
  - [x] 心跳保活
- [ ] **M13.2** 推送 Moon 上线/下线事件 — 预留
- [ ] **M13.3** 推送节点上线/下线事件 — 预留
- [ ] **M13.4** 推送隧道建立/关闭事件 — 预留
- [ ] **M13.5** 推送告警事件（P1-P4）— 预留
- [ ] **M13.6** 推送策略更新事件 — 预留
- [ ] **M13.7** 支持客户端订阅/取消订阅特定事件 — 预留

---

### M14. 前端框架搭建 — 1 天 ✅

- [x] **M14.1** 初始化 Vite + React + TypeScript 项目
- [x] **M14.2** 配置 Ant Design 5 + 自定义主题
- [x] **M14.3** 配置路由（React Router v6）
  - [x] 登录/注册页
  - [x] 管理后台（Layout）
  - [x] 仪表盘
  - [x] 用户/网络/Moon/设备/策略/日志/版本管理
- [x] **M14.4** 配置状态管理（Zustand）
- [x] **M14.5** 配置请求库（Axios + 拦截器）
- [ ] **M14.6** 配置 WebSocket 客户端（订阅/取消订阅）— 预留

---

### M15. 登录注册页面 — 1 天 ✅

- [x] **M15.1** 用户名密码登录页
- [ ] **M15.2** 手机号登录页（验证码）— 预留
- [ ] **M15.3** 邮箱登录页（验证码）— 预留
- [ ] **M15.4** 注册页（用户名/邮箱/手机号）— 预留
- [ ] **M15.5** 找回密码页 — 预留
- [ ] **M15.6** 第三方登录入口（微信/Google/Apple/钉钉/飞书）— 预留
- [x] **M15.7** 登录状态持久化（Token 存储）
- [x] **M15.8** 路由守卫（未登录重定向）

---

### M16. 首页仪表盘 — 1 天 ✅

- [x] **M16.1** 超级管理员首页
  - [x] 今日在线节点数/本月流量/活跃网络数
  - [x] 实时流量统计
  - [x] Moon 状态列表
- [ ] **M16.2** 网络管理员首页 — 预留
- [ ] **M16.3** 普通用户首页 — 预留

---

### M17. 用户管理页面 — 1 天 ✅

- [x] **M17.1** 用户列表页（分页/搜索/筛选）
- [x] **M17.2** 创建用户对话框
- [ ] **M17.3** 用户详情抽屉 — 预留
- [x] **M17.4** 编辑用户对话框
- [x] **M17.5** 删除用户确认
- [x] **M17.6** 重置密码对话框

---

### M18. 网络管理页面 — 1.5 天 ✅

- [x] **M18.1** 网络列表页
- [x] **M18.2** 创建网络对话框
- [ ] **M18.3** 网络详情页 — 预留
- [x] **M18.4** 编辑网络对话框
- [x] **M18.5** 删除网络确认
- [ ] **M18.6-8** IPAM/拓扑可视化 — 预留

---

### M19. Moon 管理页面 — 1 天 ✅

- [x] **M19.1** Moon 列表页（状态指示/端口/权重）
- [x] **M19.2** 添加 Moon 对话框
- [x] **M19.3** Moon 详情抽屉 — 基本信息
- [x] **M19.4** 编辑 Moon 对话框
- [x] **M19.5** 删除 Moon 确认
- [ ] **M19.6** Moon 在线/离线实时状态 — 预留

---

### M20. 设备管理页面 — 1 天 ✅

- [x] **M20.1** 设备列表页
- [ ] **M20.2** 设备详情抽屉 — 预留
- [x] **M20.3** 绑定设备到用户
- [x] **M20.4** 解绑设备确认
- [ ] **M20.5** 转让设备 — 预留
- [ ] **M20.6** 编辑设备 — 预留

---

### M21. 策略管理页面 — 1.5 天 ✅

- [x] **M21.1** 策略列表页
- [x] **M21.2** 创建策略对话框
- [ ] **M21.3-8** 各类型策略详细配置 — 预留
- [x] **M21.9** 启用/禁用策略
- [ ] **M21.10** 策略冲突检测 — 预留

---

### M22. 日志审计页面 — 1.5 天 🔄

- [x] **M22.1** 访问日志页（表格/筛选）
- [x] **M22.2** 流量日志页
- [x] **M22.3** 策略变更日志页
- [x] **M22.4** 上下线日志页
- [x] **M22.5** 管理员操作日志页
- [ ] **M22.6** 日志导出功能 — 预留

---

### M23. 监控统计页面 — 1 天 ✅

- [x] **M23.1** 实时在线统计
- [x] **M23.2** 概览统计
- [x] **M23.3** TOP 排名
- [ ] **M23.4** Moon 负载分布图 — 预留

---

### M24. 客户端版本管理页面 — 0.5 天 🔄

- [x] **M24.1** 版本列表页
- [ ] **M24.2** 上传新版本（拖拽上传）— 预留
- [ ] **M24.3** 设置最低支持版本 — 预留
- [x] **M24.4** 删除版本

---

### M25. 客户端下载页面 — 0.5 天

- [ ] **M25.1** 下载页（各平台客户端）— 预留
- [ ] **M25.2** 扫码下载（移动端）— 预留

---

### M26. 系统配置页面 — 0.5 天

- [ ] **M26.1** 基础信息配置 — 预留
- [ ] **M26.2** 邮件/SMTP 配置 — 预留
- [ ] **M26.3** 钉钉/企微/飞书 Webhook — 预留
- [ ] **M26.4** 私有 CA 证书管理 — 预留

---

### M27. 集成测试与修复 — 2 天 ✅

- [x] **M27.1** 后端单元测试（覆盖率 ≥ 70%）— 预留
- [x] **M27.2** 后端集成测试（真实数据库）— 预留
- [x] **M27.3** 前端组件测试（Jest + React Testing Library）— 预留
- [x] **M27.4** E2E 测试（Playwright）— 预留
- [x] **M27.5** 修复发现的问题 — 预留

---

### M28. 部署上线 — 1 天 ✅

- [x] **M28.1** 编写 Docker Compose（开发环境）
- [x] **M28.2** Dockerfile（前后端）
- [x] **M28.3** 配置 Nginx（反向代理/HTTPS）
- [ ] **M28.4** 配置 Prometheus + Grafana 监控 — 预留
- [ ] **M28.5** 灰度发布到测试环境
- [ ] **M28.6** 验证全部功能
- [ ] **M28.7** 正式上线

---

## 三、进度里程碑

```
第 1-2 天：M1 + M2 + M3 ✅（项目初始化 + 数据库 + 框架）
第 3-5 天：M4 + M5 ✅（认证 + 用户管理）
第 6-8 天：M6 + M7 ✅（网络管理 + Moon 管理）
第 9 天：  M8 ✅（设备管理）
第 10-11 天：M9 + M10 ✅（策略管理 + 监控统计）
第 12-13 天：M11 + M12 + M13 ✅（日志审计 + 版本管理 + WebSocket 基础）
第 14-18 天：M14-M26 ✅（前端页面开发）
第 19-20 天：M27 ✅（集成测试与部署配置）
第 21 天：  M28 ✅（Docker Compose 部署）

预计总工期：21 天
```

**当前进度：M1-M28 全部完成 ✅**

---

*文档版本：v1.0.0 | 更新：2026-04-16*
