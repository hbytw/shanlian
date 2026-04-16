package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// TrafficLog 流量日志
type TrafficLog struct {
	ID          int64     `json:"id"`
	PeerID      uuid.UUID `json:"peer_id"`
	SessionID   *uuid.UUID `json:"session_id,omitempty"`
	NetworkID   uuid.UUID `json:"network_id"`
	Direction   string    `json:"direction"`
	Protocol    string    `json:"protocol"`
	Bytes       int64     `json:"bytes"`
	Packets     int64     `json:"packets"`
	DurationSec int       `json:"duration_sec"`
	TunnelType  string    `json:"tunnel_type"`
	LoggedAt    time.Time `json:"logged_at"`
}

// AccessLog 访问日志
type AccessLog struct {
	ID          int64      `json:"id"`
	PeerID      uuid.UUID  `json:"peer_id"`
	DeviceID    string     `json:"device_id"`
	NetworkID   uuid.UUID  `json:"network_id"`
	SessionID   *uuid.UUID `json:"session_id,omitempty"`
	URL         string     `json:"url"`
	Host        *string    `json:"host,omitempty"`
	Path        *string    `json:"path,omitempty"`
	Method      *string    `json:"method,omitempty"`
	StatusCode  *int       `json:"status_code,omitempty"`
	BytesIn     int64      `json:"bytes_in"`
	BytesOut    int64      `json:"bytes_out"`
	DurationMs  *int       `json:"duration_ms,omitempty"`
	AccessedAt  time.Time  `json:"accessed_at"`
}

// PolicyChangeLog 策略变更日志
type PolicyChangeLog struct {
	ID          int64           `json:"id"`
	PeerID      *uuid.UUID      `json:"peer_id,omitempty"`
	DeviceID    *string         `json:"device_id,omitempty"`
	NetworkID   uuid.UUID       `json:"network_id"`
	PolicyID    uuid.UUID       `json:"policy_id"`
	PolicyName  *string         `json:"policy_name,omitempty"`
	PolicyType  *string         `json:"policy_type,omitempty"`
	Action      string          `json:"action"`
	OldConfig   json.RawMessage `json:"old_config,omitempty"`
	NewConfig   json.RawMessage `json:"new_config,omitempty"`
	Reason      *string         `json:"reason,omitempty"`
	Operator    *string         `json:"operator,omitempty"`
	OperatorID  *uuid.UUID      `json:"operator_id,omitempty"`
	ChangedAt   time.Time       `json:"changed_at"`
}

// OnlineLog 上下线日志
type OnlineLog struct {
	ID          int64     `json:"id"`
	PeerID      uuid.UUID `json:"peer_id"`
	DeviceID    string    `json:"device_id"`
	NetworkID   uuid.UUID `json:"network_id"`
	Action      string    `json:"action"`
	PublicIP    *string   `json:"public_ip,omitempty"`
	Hostname    *string   `json:"hostname,omitempty"`
	DurationSec *int      `json:"duration_sec,omitempty"`
	LoggedAt    time.Time `json:"logged_at"`
}

// AdminLog 管理员操作日志
type AdminLog struct {
	ID           uuid.UUID       `json:"id"`
	OperatorID   uuid.UUID       `json:"operator_id"`
	OperatorName *string         `json:"operator_name,omitempty"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   *uuid.UUID      `json:"resource_id,omitempty"`
	ResourceName *string         `json:"resource_name,omitempty"`
	Detail       json.RawMessage `json:"detail,omitempty"`
	IPAddress    *string         `json:"ip_address,omitempty"`
	UserAgent    *string         `json:"user_agent,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

// AuditConfig 审计服务器配置
type AuditConfig struct {
	ID               uuid.UUID  `json:"id"`
	NetworkID        *uuid.UUID `json:"network_id,omitempty"`
	AuditServer      string     `json:"audit_server"`
	AuditEnabled     bool       `json:"audit_enabled"`
	AuditTypes       []string   `json:"audit_types"`
	AuditIntervalSec int        `json:"audit_interval_sec"`
	AuditBufferMax   int        `json:"audit_buffer_max"`
	AuditAPIKey      *string    `json:"audit_api_key,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

