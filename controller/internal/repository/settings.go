package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// SettingsRepository 系统设置仓储
type SettingsRepository struct {
	db *sql.DB
}

// NewSettingsRepository 创建设置仓储
func NewSettingsRepository(db *sql.DB) *SettingsRepository {
	return &SettingsRepository{db: db}
}

// GetPlatformConfig 获取平台配置
func (r *SettingsRepository) GetPlatformConfig(ctx context.Context) (string, string, error) {
	query := `SELECT platform_name, platform_logo FROM system_settings WHERE id = 'default'`
	var name, logo sql.NullString
	err := r.db.QueryRowContext(ctx, query).Scan(&name, &logo)
	if err == sql.ErrNoRows {
		return "闪连 VPN 管理平台", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("获取平台配置失败：%w", err)
	}
	if name.Valid {
		return name.String, logo.String, nil
	}
	return "闪连 VPN 管理平台", "", nil
}

// UpdatePlatformConfig 更新平台配置
func (r *SettingsRepository) UpdatePlatformConfig(ctx context.Context, name, logo string) error {
	query := `
		INSERT INTO system_settings (id, platform_name, platform_logo)
		VALUES ('default', $1, $2)
		ON CONFLICT (id) DO UPDATE SET
			platform_name = EXCLUDED.platform_name,
			platform_logo = EXCLUDED.platform_logo
	`
	_, err := r.db.ExecContext(ctx, query, name, logo)
	if err != nil {
		return fmt.Errorf("更新平台配置失败：%w", err)
	}
	return nil
}

// GetSmtpConfig 获取 SMTP 配置
func (r *SettingsRepository) GetSmtpConfig(ctx context.Context) ([]byte, error) {
	query := `SELECT smtp_config FROM system_settings WHERE id = 'default'`
	var configJSON sql.NullString
	err := r.db.QueryRowContext(ctx, query).Scan(&configJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("获取SMTP配置失败：%w", err)
	}
	if !configJSON.Valid || configJSON.String == "" {
		return nil, nil
	}
	return []byte(configJSON.String), nil
}

// UpdateSmtpConfig 更新 SMTP 配置
func (r *SettingsRepository) UpdateSmtpConfig(ctx context.Context, config map[string]interface{}) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("序列化SMTP配置失败：%w", err)
	}
	query := `
		INSERT INTO system_settings (id, smtp_config)
		VALUES ('default', $1)
		ON CONFLICT (id) DO UPDATE SET smtp_config = EXCLUDED.smtp_config
	`
	_, err = r.db.ExecContext(ctx, query, configJSON)
	if err != nil {
		return fmt.Errorf("更新SMTP配置失败：%w", err)
	}
	return nil
}

// GetWebhookConfig 获取 Webhook 配置
func (r *SettingsRepository) GetWebhookConfig(ctx context.Context) ([]byte, error) {
	query := `SELECT webhook_config FROM system_settings WHERE id = 'default'`
	var configJSON sql.NullString
	err := r.db.QueryRowContext(ctx, query).Scan(&configJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("获取Webhook配置失败：%w", err)
	}
	if !configJSON.Valid || configJSON.String == "" {
		return nil, nil
	}
	return []byte(configJSON.String), nil
}

// UpdateWebhookConfig 更新 Webhook 配置
func (r *SettingsRepository) UpdateWebhookConfig(ctx context.Context, config map[string]interface{}) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("序列化Webhook配置失败：%w", err)
	}
	query := `
		INSERT INTO system_settings (id, webhook_config)
		VALUES ('default', $1)
		ON CONFLICT (id) DO UPDATE SET webhook_config = EXCLUDED.webhook_config
	`
	_, err = r.db.ExecContext(ctx, query, configJSON)
	if err != nil {
		return fmt.Errorf("更新Webhook配置失败：%w", err)
	}
	return nil
}

// GetCertConfig 获取证书配置
func (r *SettingsRepository) GetCertConfig(ctx context.Context) ([]byte, error) {
	query := `SELECT cert_config FROM system_settings WHERE id = 'default'`
	var configJSON sql.NullString
	err := r.db.QueryRowContext(ctx, query).Scan(&configJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("获取证书配置失败：%w", err)
	}
	if !configJSON.Valid || configJSON.String == "" {
		return nil, nil
	}
	return []byte(configJSON.String), nil
}

// UpdateCertConfig 更新证书配置
func (r *SettingsRepository) UpdateCertConfig(ctx context.Context, config map[string]interface{}) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("序列化证书配置失败：%w", err)
	}
	query := `
		INSERT INTO system_settings (id, cert_config)
		VALUES ('default', $1)
		ON CONFLICT (id) DO UPDATE SET cert_config = EXCLUDED.cert_config
	`
	_, err = r.db.ExecContext(ctx, query, configJSON)
	if err != nil {
		return fmt.Errorf("更新证书配置失败：%w", err)
	}
	return nil
}
