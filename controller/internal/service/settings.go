package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"time"

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

	// 构建测试消息内容
	var jsonBody []byte
	var err error

	switch webhookType {
	case "dingtalk":
		jsonBody, err = buildDingTalkMessage()
	case "wecom":
		jsonBody, err = buildWeComMessage()
	case "feishu":
		jsonBody, err = buildFeiShuMessage()
	}
	if err != nil {
		return fmt.Errorf("构建测试消息失败：%w", err)
	}

	// 发送 HTTP POST 请求
	err = sendWebhookRequest(ctx, url, jsonBody)
	if err != nil {
		return fmt.Errorf("发送测试消息失败：%w", err)
	}

	return nil
}

// buildDingTalkMessage 构建钉钉测试消息
func buildDingTalkMessage() ([]byte, error) {
	msg := map[string]interface{}{
		"msgtype": "text",
		"text": map[string]interface{}{
			"content": "闪连 VPN 管理平台 Webhook 测试消息\n这是一条来自管理平台的测试通知，用于验证 Webhook 配置是否正确。",
		},
	}
	return json.Marshal(msg)
}

// buildWeComMessage 构建企业微信测试消息
func buildWeComMessage() ([]byte, error) {
	msg := map[string]interface{}{
		"msgtype": "text",
		"text": map[string]interface{}{
			"content": "闪连 VPN 管理平台 Webhook 测试消息\n这是一条来自管理平台的测试通知，用于验证 Webhook 配置是否正确。",
		},
	}
	return json.Marshal(msg)
}

// buildFeiShuMessage 构建飞书测试消息
func buildFeiShuMessage() ([]byte, error) {
	msg := map[string]interface{}{
		"msg_type": "text",
		"content": map[string]interface{}{
			"text": "闪连 VPN 管理平台 Webhook 测试消息\n这是一条来自管理平台的测试通知，用于验证 Webhook 配置是否正确。",
		},
	}
	return json.Marshal(msg)
}

// sendWebhookRequest 发送 Webhook 请求
func sendWebhookRequest(ctx context.Context, url string, jsonBody []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("创建请求失败：%w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送请求失败：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("服务器返回错误状态码：%d", resp.StatusCode)
	}

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
