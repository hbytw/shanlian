package model

import (
	"time"

	"github.com/google/uuid"
)

// Version 客户端版本
type Version struct {
	ID            uuid.UUID `json:"id"`
	Version       string    `json:"version"`
	MinSupported  string    `json:"min_supported"`
	DownloadURL   string    `json:"download_url"`
	SHA256        string    `json:"sha256"`
	FileSize      int64     `json:"file_size"`
	Platform      string    `json:"platform"`
	ForceUpdate   bool      `json:"force_update"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	CreatedBy     *uuid.UUID `json:"created_by,omitempty"`
}

// VersionCreateRequest 创建版本请求
type VersionCreateRequest struct {
	Version      string `json:"version" binding:"required"`
	MinSupported string `json:"min_supported" binding:"required"`
	DownloadURL  string `json:"download_url" binding:"required,url"`
	SHA256       string `json:"sha256" binding:"required,len=64"`
	FileSize     int64  `json:"file_size" binding:"required"`
	Platform     string `json:"platform" binding:"required,oneof=linux windows darwin android ios"`
	ForceUpdate  bool   `json:"force_update"`
}

// LoginRequest 登录请求
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResponse 登录响应
type LoginResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	User         *User  `json:"user,omitempty"`
}

// RegisterRequest 注册请求
type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=64"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
	Code     string `json:"code" binding:"required"`
}

// RefreshRequest 刷新令牌请求
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// SendCodeRequest 发送验证码请求
type SendCodeRequest struct {
	Email    string `json:"email" binding:"omitempty,email"`
	Phone    string `json:"phone" binding:"omitempty"`
	CaptchaID string `json:"captcha_id"`
	CaptchaCode string `json:"captcha_code"`
}

type ResetPasswordRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Code     string `json:"code" binding:"required"`
	Password string `json:"password" binding:"required,min=8"`
}

// ClientLoginRequest 客户端登录请求
type ClientLoginRequest struct {
	DeviceID string `json:"device_id" binding:"required"`
	Token    string `json:"token"`
}

// OAuthCallbackRequest OAuth 回调请求
type OAuthCallbackRequest struct {
	Code  string `json:"code" binding:"required"`
	State string `json:"state"`
}
