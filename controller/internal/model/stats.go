package model

import (
	"time"

	"github.com/google/uuid"
)

// StatsRealtimeResponse 实时统计响应
type StatsRealtimeResponse struct {
	OnlinePeers    int64            `json:"online_peers"`
	OnlineDevices  int64            `json:"online_devices"`
	OnlineMoons    int64            `json:"online_moons"`
	TotalNetworks  int64            `json:"total_networks"`
	TrafficIn      int64            `json:"traffic_in"`
	TrafficOut     int64            `json:"traffic_out"`
	PeerDetails    []PeerStat       `json:"peer_details,omitempty"`
	NetworkStats   []NetworkStat    `json:"network_stats,omitempty"`
	MoonStats      []MoonStat       `json:"moon_stats,omitempty"`
}

// PeerStat 节点统计
type PeerStat struct {
	PeerID      uuid.UUID `json:"peer_id"`
	DeviceID    string    `json:"device_id"`
	Hostname    string    `json:"hostname"`
	NetworkID   uuid.UUID `json:"network_id"`
	NetworkName string    `json:"network_name"`
	AssignedIP  string    `json:"assigned_ip"`
	TrafficIn   int64     `json:"traffic_in"`
	TrafficOut  int64     `json:"traffic_out"`
	Online      bool      `json:"online"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// NetworkStat 网络统计
type NetworkStat struct {
	NetworkID   uuid.UUID `json:"network_id"`
	NetworkName string    `json:"network_name"`
	PeerCount   int64     `json:"peer_count"`
	OnlineCount int64     `json:"online_count"`
	TrafficIn   int64     `json:"traffic_in"`
	TrafficOut  int64     `json:"traffic_out"`
}

// MoonStat Moon 节点统计
type MoonStat struct {
	MoonID      uuid.UUID `json:"moon_id"`
	Domain      string    `json:"domain"`
	Online      bool      `json:"online"`
	Connections int       `json:"connections"`
	TrafficIn   int64     `json:"traffic_in"`
	TrafficOut  int64     `json:"traffic_out"`
}

// StatsOverviewResponse 概览统计响应
type StatsOverviewResponse struct {
	TotalUsers      int64   `json:"total_users"`
	TotalDevices    int64   `json:"total_devices"`
	TotalNetworks   int64   `json:"total_networks"`
	TotalPeers      int64   `json:"total_peers"`
	TotalMoonNodes   int64   `json:"total_moon_nodes"`
	OnlinePeers24h   int64   `json:"online_peers_24h"`
	TrafficIn24h    int64   `json:"traffic_in_24h"`
	TrafficOut24h   int64   `json:"traffic_out_24h"`
	ActiveSessions  int64   `json:"active_sessions"`
}

// StatsTrafficResponse 流量统计响应
type StatsTrafficResponse struct {
	TotalIn      int64           `json:"total_in"`
	TotalOut     int64           `json:"total_out"`
	TopPeers     []TrafficItem   `json:"top_peers"`
	TopNetworks  []TrafficItem   `json:"top_networks"`
	TopMoons     []TrafficItem   `json:"top_moons"`
	Timeline     []TrafficPoint  `json:"timeline"`
}

// TrafficItem 流量项
type TrafficItem struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	TrafficIn int64     `json:"traffic_in"`
	TrafficOut int64    `json:"traffic_out"`
}

// TrafficPoint 流量时间点
type TrafficPoint struct {
	Timestamp time.Time `json:"timestamp"`
	TrafficIn int64    `json:"traffic_in"`
	TrafficOut int64   `json:"traffic_out"`
}

// StatsTopResponse Top N 统计响应
type StatsTopResponse struct {
	TopPeers    []TopPeer    `json:"top_peers"`
	TopNetworks []TopNetwork `json:"top_networks"`
}

// TopPeer Top 节点
type TopPeer struct {
	PeerID     uuid.UUID `json:"peer_id"`
	DeviceID   string    `json:"device_id"`
	Hostname   string    `json:"hostname"`
	TrafficIn  int64     `json:"traffic_in"`
	TrafficOut int64     `json:"traffic_out"`
	OnlineTime int64     `json:"online_time"`
}

// TopNetwork Top 网络
type TopNetwork struct {
	NetworkID   uuid.UUID `json:"network_id"`
	NetworkName string    `json:"network_name"`
	PeerCount   int64     `json:"peer_count"`
	TrafficIn   int64     `json:"traffic_in"`
	TrafficOut  int64     `json:"traffic_out"`
}

// ReceiveTrafficRequest 接收流量上报请求
type ReceiveTrafficRequest struct {
	PeerID     string `json:"peer_id" binding:"required"`
	SessionID  string `json:"session_id"`
	Direction  string `json:"direction" binding:"required,oneof=in out"`
	Bytes      int64  `json:"bytes" binding:"required"`
	Packets    int64  `json:"packets"`
	Duration   int    `json:"duration"`
	Protocol   string `json:"protocol"`
	TunnelType string `json:"tunnel_type"`
}
