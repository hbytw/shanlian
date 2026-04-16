-- ============================================================
-- 闪连 VPN — 数据库索引脚本 v0.0.0
-- 迁移版本：002
-- 创建时间：2026-04-16
-- 说明：为高频查询字段创建索引
-- ============================================================

-- users 表索引
CREATE INDEX idx_users_username ON users(username);
CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_phone ON users(phone);
CREATE INDEX idx_users_role ON users(role);

-- networks 表索引
CREATE INDEX idx_networks_owner ON networks(created_by);

-- user_networks 表索引
CREATE INDEX idx_user_networks_user ON user_networks(user_id);
CREATE INDEX idx_user_networks_network ON user_networks(network_id);
CREATE INDEX idx_user_networks_role ON user_networks(role);

-- devices 表索引
CREATE INDEX idx_devices_user ON devices(user_id);
CREATE INDEX idx_devices_device_id ON devices(device_id);
CREATE INDEX idx_devices_assigned_ip ON devices(assigned_ip);
CREATE INDEX idx_devices_online ON devices(online);
CREATE INDEX idx_devices_user_online ON devices(user_id, online);

-- moon_nodes 表索引
CREATE INDEX idx_moon_nodes_domain ON moon_nodes(domain);
CREATE INDEX idx_moon_nodes_api_key ON moon_nodes(api_key);
CREATE INDEX idx_moon_nodes_online ON moon_nodes(online);

-- peers 表索引
CREATE INDEX idx_peers_device ON peers(device_id);
CREATE INDEX idx_peers_network ON peers(network_id);
CREATE INDEX idx_peers_network_online ON peers(network_id, online);
CREATE INDEX idx_peers_assigned_ip ON peers(assigned_ip);
CREATE INDEX idx_peers_online ON peers(online);

-- policies 表索引
CREATE INDEX idx_policies_network ON policies(network_id);
CREATE INDEX idx_policies_type ON policies(policy_type);
CREATE INDEX idx_policies_enabled ON policies(enabled);
CREATE INDEX idx_policies_network_enabled ON policies(network_id, enabled);

-- sessions 表索引
CREATE INDEX idx_sessions_peer ON sessions(peer_id);
CREATE INDEX idx_sessions_moon ON sessions(moon_id);
CREATE INDEX idx_sessions_active ON sessions(active);
CREATE INDEX idx_sessions_established ON sessions(established_at);

-- traffic_logs 表索引
CREATE INDEX idx_traffic_logs_peer ON traffic_logs(peer_id);
CREATE INDEX idx_traffic_logs_network ON traffic_logs(network_id);
CREATE INDEX idx_traffic_logs_logged_at ON traffic_logs(logged_at);
CREATE INDEX idx_traffic_logs_peer_logged ON traffic_logs(peer_id, logged_at);

-- access_logs 表索引
CREATE INDEX idx_access_logs_peer ON access_logs(peer_id);
CREATE INDEX idx_access_logs_device ON access_logs(device_id);
CREATE INDEX idx_access_logs_network ON access_logs(network_id);
CREATE INDEX idx_access_logs_host ON access_logs(host);
CREATE INDEX idx_access_logs_accessed_at ON access_logs(accessed_at);

-- policy_change_logs 表索引
CREATE INDEX idx_policy_change_peer ON policy_change_logs(peer_id);
CREATE INDEX idx_policy_change_network ON policy_change_logs(network_id);
CREATE INDEX idx_policy_change_policy ON policy_change_logs(policy_id);
CREATE INDEX idx_policy_change_at ON policy_change_logs(changed_at);

-- online_logs 表索引
CREATE INDEX idx_online_logs_peer ON online_logs(peer_id);
CREATE INDEX idx_online_logs_network ON online_logs(network_id);
CREATE INDEX idx_online_logs_action ON online_logs(action);
CREATE INDEX idx_online_logs_logged_at ON online_logs(logged_at);

-- admin_logs 表索引
CREATE INDEX idx_admin_logs_operator ON admin_logs(operator_id);
CREATE INDEX idx_admin_logs_resource ON admin_logs(resource_type, resource_id);
CREATE INDEX idx_admin_logs_created_at ON admin_logs(created_at);

-- versions 表索引
CREATE INDEX idx_versions_platform ON versions(platform);
CREATE INDEX idx_versions_enabled ON versions(enabled);
