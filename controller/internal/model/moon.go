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
	Version   string `json:"version"`
}

type MoonHeartbeatRequest struct {
	Domain string `json:"domain" binding:"required"`
	Status string `json:"status"`
}
