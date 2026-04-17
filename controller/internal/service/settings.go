package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/smtp"

	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/repository"
)

// SettingsService 系统设置服务
type SettingsService struct {
	settingsRepo *repository.SettingsRepository
}

// NewSettingsService 创建设置服务
func NewSettingsService(settingsRepo *repository.SettingsRepository) *SettingsService {
	return &SettingsService{settingsRepo: settingsRepo}
}

// GetPlatformConfig 获取平台配置
func (s *SettingsService) GetPlatformConfig(ctx context.Context) (*model.PlatformConfig, error) {
	name, logo, err := s.settingsRepo.GetPlatformConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &model.PlatformConfig{
		PlatformName: name,
		PlatformLogo: logo,
	}, nil
}

// UpdatePlatformConfig 更新平台配置
func (s *SettingsService) UpdatePlatformConfig(ctx context.Context, config *model.PlatformConfig) error {
	if config.PlatformName == "" {
		config.PlatformName = "闪连 VPN 管理平台"
	}
	return s.settingsRepo.UpdatePlatformConfig(ctx, config.PlatformName, config.PlatformLogo)
}

// GetSmtpConfig 获取 SMTP 配置
func (s *SettingsService) GetSmtpConfig(ctx context.Context) (*model.SmtpConfig, error) {
	data, err := s.settingsRepo.GetSmtpConfig(ctx)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return &model.SmtpConfig{
			Host:     "smtp.example.com",
			Port:     587,
			Username: "",
			Password: "",
			FromEmail: "",
			UseTLS:   true,
		}, nil
	}
	var config model.SmtpConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析SMTP配置失败：%w", err)
	}
	config.Password = "" // 不返回密码
	return &config, nil
}

// UpdateSmtpConfig 更新 SMTP 配置
func (s *SettingsService) UpdateSmtpConfig(ctx context.Context, config *model.SmtpConfig) error {
	configMap := map[string]interface{}{
		"host":      config.Host,
		"port":      config.Port,
		"username":  config.Username,
		"password":  config.Password,
		"from_email": config.FromEmail,
		"use_tls":   config.UseTLS,
	}
	return s.settingsRepo.UpdateSmtpConfig(ctx, configMap)
}

// TestSmtpConfig 测试 SMTP 配置
func (s *SettingsService) TestSmtpConfig(ctx context.Context, config *model.SmtpConfig) error {
	if config.Host == "" || config.Port == 0 || config.Username == "" || config.FromEmail == "" {
		return fmt.Errorf("请填写完整的SMTP配置")
	}

	addr := fmt.Sprintf("%s:%d", config.Host, config.Port)

	var auth smtp.Auth
	if config.UseTLS {
		// 使用 TLS
		auth = smtp.PlainAuth("", config.Username, config.Password, config.Host)
	} else {
		auth = smtp.PlainAuth("", config.Username, config.Password, config.Host)
	}

	err := smtp.SendMail(addr, auth, config.Username, []string{config.Username}, []byte("Subject: Test Email\r\n\r\nThis is a test email from VPN Admin."))
	if err != nil {
		return fmt.Errorf("发送测试邮件失败：%w", err)
	}
	return nil
}

// GetWebhookConfig 获取 Webhook 配置
func (s *SettingsService) GetWebhookConfig(ctx context.Context) (*model.WebhookConfig, error) {
	data, err := s.settingsRepo.GetWebhookConfig(ctx)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return &model.WebhookConfig{}, nil
	}
	var config model.WebhookConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析Webhook配置失败：%w", err)
	}
	return &config, nil
}

// UpdateWebhookConfig 更新 Webhook 配置
func (s *SettingsService) UpdateWebhookConfig(ctx context.Context, config *model.WebhookConfig) error {
	configMap := map[string]interface{}{
		"dingtalk": config.DingTalk,
		"wecom":    config.WeCom,
		"feishu":   config.FeiShu,
	}
	return s.settingsRepo.UpdateWebhookConfig(ctx, configMap)
}

// TestWebhook 测试 Webhook
func (s *SettingsService) TestWebhook(ctx context.Context, webhookType, url string) error {
	if url == "" {
		return fmt.Errorf("请先配置 Webhook URL")
	}

	// 验证 URL 格式
	switch webhookType {
	case "dingtalk", "wecom", "feishu":
		// 允许的 Webhook 类型
	default:
		return fmt.Errorf("未知的 Webhook 类型")
	}

	// TODO: 实现实际的 HTTP 请求发送
	// 当前返回成功，实际使用时应调用 HTTP 客户端发送测试请求
	return nil
}

// GetCertConfig 获取证书配置
func (s *SettingsService) GetCertConfig(ctx context.Context) (*model.CertConfig, error) {
	data, err := s.settingsRepo.GetCertConfig(ctx)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return &model.CertConfig{}, nil
	}
	var config model.CertConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析证书配置失败：%w", err)
	}
	// 不返回私钥
	config.PrivateKey = ""
	return &config, nil
}

// UpdateCertConfig 更新证书配置
func (s *SettingsService) UpdateCertConfig(ctx context.Context, config *model.CertConfig) error {
	configMap := map[string]interface{}{
		"ca_cert":      config.CACert,
		"private_key":  config.PrivateKey,
		"certificate":  config.Certificate,
	}
	return s.settingsRepo.UpdateCertConfig(ctx, configMap)
}

// VerifyCert 验证证书
func (s *SettingsService) VerifyCert(ctx context.Context, config *model.CertConfig) error {
	if config.CACert == "" || config.Certificate == "" {
		return fmt.Errorf("请先配置 CA 证书和服务器证书")
	}
	// 证书验证逻辑可以在这里实现
	// 目前简化处理
	return nil
}
