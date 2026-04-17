package model

import (
	"time"

	"github.com/google/uuid"
)

type Peer struct {
	ID          uuid.UUID  `json:"id"`
	DeviceID    uuid.UUID  `json:"device_id"`
	NetworkID   uuid.UUID  `json:"network_id"`
	PublicIP    *string    `json:"public_ip,omitempty"`
	PublicPort  *int       `json:"public_port,omitempty"`
	NATType     string     `json:"nat_type"`
	Online      bool       `json:"online"`
	AssignedIP  string     `json:"assigned_ip"`
	Hostname    *string    `json:"hostname,omitempty"`
	TunnelType  string     `json:"tunnel_type"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
}

type PeerRegisterRequest struct {
	DeviceID   string `json:"device_id" binding:"required"`
	NetworkID  string `json:"network_id" binding:"required"`
	PublicIP   string `json:"public_ip"`
	PublicPort int    `json:"public_port"`
	NATType    string `json:"nat_type"`
	Hostname   string `json:"hostname"`
	TunnelType string `json:"tunnel_type"`
}

type PeerHeartbeatRequest struct {
	PeerID       string         `json:"peer_id" binding:"required"`
	Status       string         `json:"status"`
	TunnelType   string         `json:"tunnel_type"`
	TunnelQuality *TunnelQuality `json:"tunnel_quality"`
}

type TunnelQuality struct {
	RTTMs      float64 `json:"rtt_ms"`
	LossRate   float64 `json:"loss_rate"`
	RecvBytes  int64   `json:"recv_bytes"`
	SendBytes  int64   `json:"send_bytes"`
}

type PeerDiscoverRequest struct {
	NetworkID string `json:"network_id" binding:"required"`
	PeerIP    string `json:"peer_ip"`
}

type PeerDiscoverResponse struct {
	Peers []PeerInfo `json:"peers"`
}

type PeerInfo struct {
	PeerID    string `json:"peer_id"`
	PublicIP  string `json:"public_ip"`
	PublicPort int   `json:"public_port"`
	NATType   string `json:"nat_type"`
	Online    bool   `json:"online"`
}

// PeerRegisterResponse Peer注册响应
type PeerRegisterResponse struct {
	PeerID      string         `json:"peer_id"`
	AssignedIP  string         `json:"assigned_ip"`
	NetworkInfo *NetworkInfo   `json:"network_info"`
	PolicyList  []PolicyInfo   `json:"policy_list"`
	MoonList    []MoonInfo     `json:"moon_list"`
}

type NetworkInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	CIDR     string `json:"cidr"`
	GatewayIP string `json:"gateway_ip"`
}

type PolicyInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Priority int    `json:"priority"`
	Action   string `json:"action"`
}

type MoonInfo struct {
	ID       string `json:"id"`
	Domain   string `json:"domain"`
	PublicIP string `json:"public_ip"`
	RelayPort int   `json:"relay_port"`
	WSSPort   int   `json:"wss_port"`
	Weight    int   `json:"weight"`
	Online    bool   `json:"online"`
}

// PeerHeartbeatResponse Peer心跳响应
type PeerHeartbeatResponse struct {
	Status         string       `json:"status"`
	MoonList       []MoonInfo  `json:"moon_list"`
	PolicyUpdates  []PolicyInfo `json:"policy_updates"`
}
