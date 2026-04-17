-- 添加系统设置表
CREATE TABLE IF NOT EXISTS system_settings (
    id VARCHAR(64) PRIMARY KEY DEFAULT 'default',
    platform_name VARCHAR(255) NOT NULL DEFAULT '闪连 VPN 管理平台',
    platform_logo TEXT,
    smtp_config JSONB,
    webhook_config JSONB,
    cert_config JSONB,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- 初始化默认设置
INSERT INTO system_settings (id, platform_name) VALUES ('default', '闪连 VPN 管理平台')
ON CONFLICT (id) DO NOTHING;
