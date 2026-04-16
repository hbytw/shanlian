package model

import (
	"github.com/google/uuid"
)

// ErrorCode 错误码
const (
	// 认证相关 1001-1999
	ErrAuthInvalidCredentials    = "AUTH_INVALID_CREDENTIALS"
	ErrAuthTokenExpired         = "AUTH_TOKEN_EXPIRED"
	ErrAuthTokenInvalid         = "AUTH_TOKEN_INVALID"
	ErrAuthMissingToken         = "AUTH_MISSING_TOKEN"
	ErrAuthInsufficientPerms    = "AUTH_INSUFFICIENT_PERMS"
	ErrAuthAccountDisabled      = "AUTH_ACCOUNT_DISABLED"
	ErrAuthCodeExpired          = "AUTH_CODE_EXPIRED"
	ErrAuthCodeInvalid          = "AUTH_CODE_INVALID"
	ErrAuthRateLimitExceeded    = "AUTH_RATE_LIMIT_EXCEEDED"
	ErrAuthMissingSignature     = "AUTH_MISSING_SIGNATURE"
	ErrAuthInvalidSignature     = "AUTH_INVALID_SIGNATURE"
	ErrAuthTimestampExpired     = "AUTH_TIMESTAMP_EXPIRED"
	ErrAuthAPIKeyNotFound       = "AUTH_API_KEY_NOT_FOUND"
	ErrAuthAPIKeyRevoked        = "AUTH_API_KEY_REVOKED"

	// 设备相关 2001-2999
	ErrDeviceNotFound      = "DEVICE_NOT_FOUND"
	ErrDeviceAlreadyBound  = "DEVICE_ALREADY_BOUND"
	ErrDeviceNotBound      = "DEVICE_NOT_BOUND"
	ErrDeviceDisabled      = "DEVICE_DISABLED"

	// 网络相关 3001-3999
	ErrNetworkNotFound      = "NETWORK_NOT_FOUND"
	ErrNetworkDisabled      = "NETWORK_DISABLED"
	ErrNetworkCIDRConflict  = "NETWORK_CIDR_CONFLICT"
	ErrNetworkNoAvailableIP = "NETWORK_NO_AVAILABLE_IP"

	// 节点相关 2501-2999
	ErrPeerNotFound    = "PEER_NOT_FOUND"
	ErrPeerOffline     = "PEER_OFFLINE"
	ErrPeerDisabled    = "PEER_DISABLED"

	// 策略相关 2601-2699
	ErrPolicyNotFound   = "POLICY_NOT_FOUND"
	ErrPolicyDuplicate  = "POLICY_DUPLICATE"
	ErrPolicyConflict   = "POLICY_CONFLICT"

	// Moon 相关 4001-4999
	ErrMoonNotFound       = "MOON_NOT_FOUND"
	ErrMoonOffline        = "MOON_OFFLINE"
	ErrMoonAPIKeyInvalid  = "MOON_API_KEY_INVALID"
	ErrMoonDuplicateDomain = "MOON_DUPLICATE_DOMAIN"

	// 租户相关 5001-5999
	ErrUserNotFound      = "USER_NOT_FOUND"
	ErrUserAlreadyExists = "USER_ALREADY_EXISTS"
	ErrUserDisabled      = "USER_DISABLED"

	// 系统相关 9001-9999
	ErrSystemInternal       = "SYSTEM_INTERNAL_ERROR"
	ErrSystemDBError        = "SYSTEM_DB_ERROR"
	ErrSystemRedisError     = "SYSTEM_REDIS_ERROR"
	ErrSystemRateLimited    = "RATE_LIMIT_EXCEEDED"
	ErrSystemValidatationFailed = "VALIDATION_FIELD_MISSING"
)

// ErrorResponse 错误响应
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail 错误详情
type ErrorDetail struct {
	Code     string      `json:"code"`
	Message  string      `json:"message"`
	Detail   interface{} `json:"detail,omitempty"`
	RequestID string     `json:"request_id,omitempty"`
}

// SuccessResponse 成功响应
type SuccessResponse struct {
	Code    string      `json:"code,omitempty"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Meta    *Meta       `json:"meta,omitempty"`
}

// Meta 分页元数据
type Meta struct {
	Total    int64 `json:"total,omitempty"`
	Page     int   `json:"page,omitempty"`
	Limit    int   `json:"limit,omitempty"`
}

// NewErrorResponse 创建错误响应
func NewErrorResponse(code, message string, detail interface{}) ErrorResponse {
	return ErrorResponse{
		Error: ErrorDetail{
			Code:     code,
			Message:  message,
			Detail:   detail,
			RequestID: "req_" + uuid.New().String()[:8],
		},
	}
}

// PageParams 分页参数
type PageParams struct {
	Page   int    `form:"page" binding:"omitempty,min=1"`
	Limit  int    `form:"limit" binding:"omitempty,min=1,max=100"`
	Search string `form:"search"`
	Sort   string `form:"sort"`
	Order  string `form:"order" binding:"omitempty,oneof=asc desc"`
	Role   string `form:"role"` // 用于权限过滤
}

// GetOffset 计算偏移量
func (p *PageParams) GetOffset() int {
	if p.Page <= 0 {
		p.Page = 1
	}
	if p.Limit <= 0 {
		p.Limit = 20
	}
	return (p.Page - 1) * p.Limit
}
