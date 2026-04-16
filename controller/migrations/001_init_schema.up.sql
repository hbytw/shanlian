-- ============================================================
-- 闪连 VPN — 数据库初始化脚本 v0.0.0
-- 迁移版本：001
-- 创建时间：2026-04-16
-- 说明：初始化所有表结构
-- ============================================================

-- 1. users（用户表）
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username VARCHAR(64) UNIQUE NOT NULL,
    email VARCHAR(255) UNIQUE NOT NULL,
    phone VARCHAR(32) UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    role VARCHAR(32) NOT NULL DEFAULT 'user' CHECK (role IN ('super_admin', 'network_admin', 'user')),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 2. networks（网络表）
CREATE TABLE IF NOT EXISTS networks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(128) NOT NULL,
    cidr VARCHAR(24) NOT NULL,
    gateway_ip VARCHAR(16) NOT NULL,
    description TEXT,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by UUID REFERENCES users(id) ON DELETE SET NULL
);

-- 3. user_networks（用户-网络分配表）
CREATE TABLE IF NOT EXISTS user_networks (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    network_id UUID NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    role VARCHAR(32) NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'admin', 'member')),
    policy_id UUID,  -- 可选：用户在网络中的默认策略
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, network_id)
);

-- 4. devices（设备表）
CREATE TABLE IF NOT EXISTS devices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id VARCHAR(64) UNIQUE NOT NULL,  -- DID_ 前缀的设备唯一标识
    user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    hostname VARCHAR(128),
    public_ip VARCHAR(45),
    nat_type VARCHAR(32) DEFAULT 'UNKNOWN',
    assigned_ip VARCHAR(16),
    online BOOLEAN NOT NULL DEFAULT FALSE,
    client_version VARCHAR(32),
    os_type VARCHAR(32),
    os_version VARCHAR(64),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 5. moon_nodes（Moon 节点表）
CREATE TABLE IF NOT EXISTS moon_nodes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    domain VARCHAR(255) UNIQUE NOT NULL,
    public_ip VARCHAR(45) NOT NULL,
    gateway_ip VARCHAR(16) NOT NULL,
    relay_port INTEGER NOT NULL DEFAULT 11002,
    wss_port INTEGER NOT NULL DEFAULT 11443,
    udp_port INTEGER NOT NULL DEFAULT 11001,
    health_port INTEGER NOT NULL DEFAULT 11003,
    weight INTEGER NOT NULL DEFAULT 100,
    api_key VARCHAR(64) UNIQUE NOT NULL,
    api_secret VARCHAR(128) NOT NULL,
    online BOOLEAN NOT NULL DEFAULT FALSE,
    version VARCHAR(32),
    connections INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_heartbeat_at TIMESTAMPTZ
);

-- 6. peers（节点表，表示在线的设备实例）
CREATE TABLE IF NOT EXISTS peers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    network_id UUID NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    public_ip VARCHAR(45),
    public_port INTEGER,
    nat_type VARCHAR(32) DEFAULT 'UNKNOWN',
    online BOOLEAN NOT NULL DEFAULT FALSE,
    assigned_ip VARCHAR(16) NOT NULL,
    hostname VARCHAR(128),
    tunnel_type VARCHAR(16) DEFAULT 'UDP',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ
);

-- 7. policies（策略表）
CREATE TABLE IF NOT EXISTS policies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    network_id UUID NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    name VARCHAR(128) NOT NULL,
    policy_type VARCHAR(32) NOT NULL CHECK (policy_type IN ('route', 'dns', 'url_filter', 'traffic_limit', 'time_range', 'audit')),
    config JSONB NOT NULL DEFAULT '{}',
    priority INTEGER NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by UUID REFERENCES users(id) ON DELETE SET NULL
);

-- 8. sessions（会话表）
CREATE TABLE IF NOT EXISTS sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    peer_id UUID NOT NULL REFERENCES peers(id) ON DELETE CASCADE,
    moon_id UUID REFERENCES moon_nodes(id) ON DELETE SET NULL,
    tunnel_type VARCHAR(16) NOT NULL DEFAULT 'UDP',
    local_sock VARCHAR(128),
    remote_sock VARCHAR(128),
    session_key TEXT,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    established_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at TIMESTAMPTZ,
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 9. traffic_logs（流量日志表）
CREATE TABLE IF NOT EXISTS traffic_logs (
    id BIGSERIAL PRIMARY KEY,
    peer_id UUID NOT NULL REFERENCES peers(id) ON DELETE CASCADE,
    session_id UUID REFERENCES sessions(id) ON DELETE SET NULL,
    network_id UUID NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    direction VARCHAR(4) NOT NULL CHECK (direction IN ('in', 'out')),
    protocol VARCHAR(16) NOT NULL DEFAULT 'TCP',
    bytes BIGINT NOT NULL DEFAULT 0,
    packets BIGINT NOT NULL DEFAULT 0,
    duration_sec INTEGER NOT NULL DEFAULT 0,
    tunnel_type VARCHAR(16) NOT NULL DEFAULT 'UDP',
    logged_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 10. access_logs（访问日志表，客户端上报）
CREATE TABLE IF NOT EXISTS access_logs (
    id BIGSERIAL PRIMARY KEY,
    peer_id UUID NOT NULL,
    device_id VARCHAR(64) NOT NULL,
    network_id UUID NOT NULL,
    session_id UUID,
    url TEXT NOT NULL,
    host VARCHAR(255),
    path TEXT,
    method VARCHAR(16),
    status_code INTEGER,
    bytes_in BIGINT DEFAULT 0,
    bytes_out BIGINT DEFAULT 0,
    duration_ms INTEGER,
    accessed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 11. policy_change_logs（策略变更日志）
CREATE TABLE IF NOT EXISTS policy_change_logs (
    id BIGSERIAL PRIMARY KEY,
    peer_id UUID,
    device_id VARCHAR(64),
    network_id UUID NOT NULL,
    policy_id UUID NOT NULL,
    policy_name VARCHAR(128),
    policy_type VARCHAR(32),
    action VARCHAR(32) NOT NULL,
    old_config JSONB,
    new_config JSONB,
    reason TEXT,
    operator VARCHAR(128),
    operator_id UUID,
    changed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 12. online_logs（上下线日志）
CREATE TABLE IF NOT EXISTS online_logs (
    id BIGSERIAL PRIMARY KEY,
    peer_id UUID NOT NULL,
    device_id VARCHAR(64) NOT NULL,
    network_id UUID NOT NULL,
    action VARCHAR(16) NOT NULL CHECK (action IN ('online', 'offline')),
    public_ip VARCHAR(45),
    hostname VARCHAR(128),
    duration_sec INTEGER,  -- 在线时长（offline 时记录）
    logged_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 13. admin_logs（管理员操作日志）
CREATE TABLE IF NOT EXISTS admin_logs (
    id BIGSERIAL PRIMARY KEY,
    operator_id UUID NOT NULL,
    operator_name VARCHAR(128),
    action VARCHAR(64) NOT NULL,
    resource_type VARCHAR(32) NOT NULL,
    resource_id UUID,
    resource_name VARCHAR(128),
    detail JSONB,
    ip_address VARCHAR(45),
    user_agent TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 14. versions（客户端版本管理）
CREATE TABLE IF NOT EXISTS versions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    version VARCHAR(32) NOT NULL UNIQUE,
    min_supported VARCHAR(32) NOT NULL,
    download_url TEXT NOT NULL,
    sha256 VARCHAR(64) NOT NULL,
    file_size BIGINT NOT NULL DEFAULT 0,
    platform VARCHAR(16) NOT NULL CHECK (platform IN ('linux', 'windows', 'darwin', 'android', 'ios')),
    force_update BOOLEAN NOT NULL DEFAULT FALSE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by UUID REFERENCES users(id) ON DELETE SET NULL
);

-- 15. audit_configs（审计服务器配置）
CREATE TABLE IF NOT EXISTS audit_configs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    network_id UUID REFERENCES networks(id) ON DELETE CASCADE,
    audit_server VARCHAR(255) NOT NULL,
    audit_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    audit_types TEXT[] NOT NULL DEFAULT ARRAY['access', 'traffic', 'policy_change', 'tunnel', 'online'],
    audit_interval_sec INTEGER NOT NULL DEFAULT 60,
    audit_buffer_max INTEGER NOT NULL DEFAULT 1000,
    audit_api_key VARCHAR(128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (network_id)
);

-- 16. alert_configs（告警配置）
CREATE TABLE IF NOT EXISTS alert_configs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    network_id UUID REFERENCES networks(id) ON DELETE CASCADE,
    channel VARCHAR(32) NOT NULL CHECK (channel IN ('smtp', 'dingtalk', 'wecom', 'feishu')),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    config JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (network_id, channel)
);

-- ============================================================
-- 初始化种子数据
-- ============================================================

-- 创建超级管理员（密码：Admin@123456）
INSERT INTO users (id, username, email, password_hash, role)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'admin',
    'admin@shanlian.com',
    '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy',  -- Admin@123456
    'super_admin'
);

-- 创建默认网络
INSERT INTO networks (id, name, cidr, gateway_ip, description, created_by)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    '默认网络',
    '10.8.0.0/16',
    '10.8.0.1',
    '系统默认网络，供新用户使用',
    '00000000-0000-0000-0000-000000000001'
);

-- 创建测试网络管理员
INSERT INTO users (id, username, email, password_hash, role)
VALUES (
    '00000000-0000-0000-0000-000000000002',
    'netadmin',
    'netadmin@shanlian.com',
    '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy',
    'network_admin'
);

-- 将测试网络管理员分配到默认网络（owner）
INSERT INTO user_networks (user_id, network_id, role)
VALUES (
    '00000000-0000-0000-0000-000000000002',
    '00000000-0000-0000-0000-000000000001',
    'owner'
);

-- ============================================================
-- 创建更新时间戳触发器函数
-- ============================================================
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 为需要自动更新 updated_at 的表创建触发器
CREATE TRIGGER update_users_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_networks_updated_at BEFORE UPDATE ON networks
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_devices_updated_at BEFORE UPDATE ON devices
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_moon_nodes_updated_at BEFORE UPDATE ON moon_nodes
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_peers_updated_at BEFORE UPDATE ON peers
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_policies_updated_at BEFORE UPDATE ON policies
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
