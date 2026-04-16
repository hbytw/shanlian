-- ============================================================
-- 闪连 VPN — 数据库回滚脚本 v0.0.0
-- 迁移版本：001
-- 回滚时间：2026-04-16
-- 说明：删除所有表结构（慎用）
-- ============================================================

DROP TRIGGER IF EXISTS update_users_updated_at ON users;
DROP TRIGGER IF EXISTS update_networks_updated_at ON networks;
DROP TRIGGER IF EXISTS update_devices_updated_at ON devices;
DROP TRIGGER IF EXISTS update_moon_nodes_updated_at ON moon_nodes;
DROP TRIGGER IF EXISTS update_peers_updated_at ON peers;
DROP TRIGGER IF EXISTS update_policies_updated_at ON policies;

DROP TRIGGER IF EXISTS update_audit_configs_updated_at ON audit_configs;
DROP TRIGGER IF EXISTS update_alert_configs_updated_at ON alert_configs;

DROP FUNCTION IF EXISTS update_updated_at_column();

DROP TABLE IF EXISTS alert_configs;
DROP TABLE IF EXISTS audit_configs;
DROP TABLE IF EXISTS versions;
DROP TABLE IF EXISTS admin_logs;
DROP TABLE IF EXISTS online_logs;
DROP TABLE IF EXISTS policy_change_logs;
DROP TABLE IF EXISTS access_logs;
DROP TABLE IF EXISTS traffic_logs;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS policies;
DROP TABLE IF EXISTS peers;
DROP TABLE IF EXISTS moon_nodes;
DROP TABLE IF EXISTS devices;
DROP TABLE IF EXISTS user_networks;
DROP TABLE IF EXISTS networks;
DROP TABLE IF EXISTS users;
