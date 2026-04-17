package model

import (
	"time"

	"github.com/google/uuid"
)

type MoonNode struct {
	ID              uuid.UUID  `json:"id"`
	Domain          string     `json:"domain"`
	PublicIP        string     `json:"public_ip"`
	GatewayIP       string     `json:"gateway_ip"`
	RelayPort       int        `json:"relay_port"`
	WSSPort         int        `json:"wss_port"`
	UDPPort         int        `json:"udp_port"`
	HealthPort      int        `json:"health_port"`
	Weight          int        `json:"weight"`
	APIKey          string     `json:"api_key"`
	APISecret       string     `json:"-"`
	Online          bool       `json:"online"`
	Version         *string    `json:"version,omitempty"`
	Connections     int        `json:"connections"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
}

type MoonCreateRequest struct {
	Domain     string `json:"domain" binding:"required"`
	PublicIP   string `json:"public_ip" binding:"required"`
	GatewayIP  string `json:"gateway_ip" binding:"required"`
	RelayPort  int    `json:"relay_port"`
	WSSPort    int    `json:"wss_port"`
	UDPPort    int    `json:"udp_port"`
	HealthPort int    `json:"health_port"`
	Weight     int    `json:"weight"`
}

type MoonUpdateRequest struct {
	PublicIP   string `json:"public_ip"`
	GatewayIP  string `json:"gateway_ip"`
	RelayPort  *int   `json:"relay_port"`
	WSSPort    *int   `json:"wss_port"`
	UDPPort    *int   `json:"udp_port"`
	HealthPort *int   `json:"health_port"`
	Weight     *int   `json:"weight"`
}

type MoonRegisterRequest struct {
	Domain    string `json:"domain" binding:"required"`
	PublicIP  string `json:"public_ip" binding:"required"`
	GatewayIP string `json:"gateway_ip" binding:"required"`
	UDPPort   int    `json:"udp_port" binding:"required"`
	RelayPort int    `json:"relay_port" binding:"required"`
	WSSPort   int    `json:"wss_port" binding:"required"`
	Version   string `json:"version"`
}

type MoonHeartbeatRequest struct {
	Domain     string `json:"domain" binding:"required"`
	Status     string `json:"status"`
	Connections int   `json:"connections"`
}

// MoonRegisterResponse Moon注册响应（首次注册返回完整密钥）
type MoonRegisterResponse struct {
	ID          uuid.UUID `json:"id"`
	Domain      string    `json:"domain"`
	PublicIP    string    `json:"public_ip"`
	GatewayIP   string    `json:"gateway_ip"`
	RelayPort   int       `json:"relay_port"`
	WSSPort     int       `json:"wss_port"`
	UDPPort     int       `json:"udp_port"`
	HealthPort  int       `json:"health_port"`
	Weight      int       `json:"weight"`
	APIKey      string    `json:"api_key"`
	APISecret   string    `json:"api_secret"` // 首次注册时返回，后续不再返回
	Online      bool      `json:"online"`
	Version     *string   `json:"version,omitempty"`
}

// MoonHeartbeatResponse Moon心跳响应
type MoonHeartbeatResponse struct {
	Status      string `json:"status"`
	MoonCount   int    `json:"moon_count"`    // 当前在线Moon数量
	Shutdown    bool   `json:"shutdown"`      // 是否关闭指令
	NextKey     string `json:"next_key,omitempty"` // 即将轮换的新API Key（有的话）
}
