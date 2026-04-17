package model

// PlatformConfig 平台配置
type PlatformConfig struct {
	PlatformName string `json:"platform_name"`
	PlatformLogo string `json:"platform_logo,omitempty"`
}

// SmtpConfig SMTP 配置
type SmtpConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	FromEmail string `json:"from_email"`
	UseTLS   bool   `json:"use_tls"`
}

// WebhookConfig Webhook 配置
type WebhookConfig struct {
	DingTalk string `json:"dingtalk,omitempty"`
	WeCom    string `json:"wecom,omitempty"`
	FeiShu   string `json:"feishu,omitempty"`
}

// CertConfig 证书配置
type CertConfig struct {
	CACert       string `json:"ca_cert,omitempty"`
	PrivateKey   string `json:"private_key,omitempty"`
	Certificate  string `json:"certificate,omitempty"`
}

// Settings 系统设置
type Settings struct {
	ID        string       `json:"id"`
	Platform  PlatformConfig `json:"platform"`
	Smtp      SmtpConfig   `json:"smtp"`
	Webhook   WebhookConfig `json:"webhook"`
}
