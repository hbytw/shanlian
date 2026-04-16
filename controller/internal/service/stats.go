package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/repository"
)

// StatsService 统计服务
type StatsService struct {
	peerRepo    *repository.PeerRepository
	deviceRepo  *repository.DeviceRepository
	moonRepo    *repository.MoonRepository
	networkRepo *repository.NetworkRepository
	auditRepo   *repository.AuditRepository
}

func NewStatsService(peerRepo *repository.PeerRepository, deviceRepo *repository.DeviceRepository, moonRepo *repository.MoonRepository, networkRepo *repository.NetworkRepository, auditRepo *repository.AuditRepository) *StatsService {
	return &StatsService{
		peerRepo:    peerRepo,
		deviceRepo:  deviceRepo,
		moonRepo:    moonRepo,
		networkRepo: networkRepo,
		auditRepo:   auditRepo,
	}
}

// TrafficQueryParams 流量查询参数
type TrafficQueryParams struct {
	StartTime time.Time
	EndTime   time.Time
	NetworkID *uuid.UUID
	PeerID    *uuid.UUID
	GroupBy   string // "hour", "day", "month"
}

// Realtime 获取实时统计
func (s *StatsService) Realtime(ctx context.Context) (*model.StatsRealtimeResponse, error) {
	// 获取在线节点
	onlinePeers, err := s.peerRepo.GetOnlinePeers(ctx)
	if err != nil {
		return nil, err
	}

	// 统计在线设备数（通过节点）
	onlineDevices := int64(0)
	deviceSet := make(map[string]bool)
	for _, peer := range onlinePeers {
		deviceSet[peer.DeviceID.String()] = true
	}
	onlineDevices = int64(len(deviceSet))

	// 统计在线 Moon 数
	moons, _, err := s.moonRepo.List(ctx, &model.PageParams{Limit: 1000})
	if err != nil {
		return nil, err
	}
	onlineMoons := int64(0)
	for _, moon := range moons {
		if moon.Online {
			onlineMoons++
		}
	}

	// 获取网络总数
	_, totalNetworks, err := s.networkRepo.List(ctx, &model.PageParams{Limit: 1})
	if err != nil {
		return nil, err
	}

	// 获取流量统计
	trafficIn, trafficOut, err := s.auditRepo.GetTrafficSummary(ctx, nil, nil, time.Now().Add(-24*time.Hour), time.Now())
	if err != nil {
		trafficIn, trafficOut = 0, 0
	}

	return &model.StatsRealtimeResponse{
		OnlinePeers:   int64(len(onlinePeers)),
		OnlineDevices: onlineDevices,
		OnlineMoons:   onlineMoons,
		TotalNetworks: totalNetworks,
		TrafficIn:     trafficIn,
		TrafficOut:    trafficOut,
		PeerDetails:   nil,
		NetworkStats:   nil,
		MoonStats:     nil,
	}, nil
}

// Overview 获取概览统计
func (s *StatsService) Overview(ctx context.Context) (*model.StatsOverviewResponse, error) {
	var totalUsers, totalDevices, totalNetworks, totalPeers, totalMoons int64
	var err error

	// 统计用户数
	_, totalUsers, err = s.auditRepo.GetUserStats(ctx)
	if err != nil {
		totalUsers = 0
	}

	// 统计设备数
	_, totalDevices, err = s.auditRepo.GetDeviceStats(ctx)
	if err != nil {
		totalDevices = 0
	}

	// 统计网络数
	_, totalNetworks, err = s.networkRepo.List(ctx, &model.PageParams{Limit: 1})
	if err != nil {
		totalNetworks = 0
	}

	// 统计节点数
	_, totalPeers, err = s.peerRepo.List(ctx, &model.PageParams{Limit: 1})
	if err != nil {
		totalPeers = 0
	}

	// 统计 Moon 数
	_, totalMoons, err = s.moonRepo.List(ctx, &model.PageParams{Limit: 1})
	if err != nil {
		totalMoons = 0
	}

	// 统计 24 小时在线节点数
	onlinePeers24h := int64(0)

	// 统计 24 小时流量
	trafficIn24h, trafficOut24h, _ := s.auditRepo.GetTrafficSummary(ctx, nil, nil, time.Now().Add(-24*time.Hour), time.Now())

	return &model.StatsOverviewResponse{
		TotalUsers:     totalUsers,
		TotalDevices:   totalDevices,
		TotalNetworks:  totalNetworks,
		TotalPeers:     totalPeers,
		TotalMoonNodes: totalMoons,
		OnlinePeers24h: onlinePeers24h,
		TrafficIn24h:   trafficIn24h,
		TrafficOut24h:  trafficOut24h,
	}, nil
}

// Traffic 获取流量统计（支持时间范围）
func (s *StatsService) Traffic(ctx context.Context, params *TrafficQueryParams) (*model.StatsTrafficResponse, error) {
	if params == nil {
		params = &TrafficQueryParams{
			StartTime: time.Now().Add(-24 * time.Hour),
			EndTime:   time.Now(),
			GroupBy:   "hour",
		}
	}

	// 获取总流量
	totalIn, totalOut, err := s.auditRepo.GetTrafficSummary(ctx, params.NetworkID, params.PeerID, params.StartTime, params.EndTime)
	if err != nil {
		return nil, err
	}

	// 获取 Top 节点流量
	topPeers, err := s.auditRepo.GetTopPeersTraffic(ctx, params.NetworkID, params.StartTime, params.EndTime, 10)
	if err != nil {
		topPeers = nil
	}

	// 获取 Top 网络流量
	topNetworks, err := s.auditRepo.GetTopNetworksTraffic(ctx, params.StartTime, params.EndTime, 10)
	if err != nil {
		topNetworks = nil
	}

	// 获取流量时间线
	timeline, err := s.auditRepo.GetTrafficTimeline(ctx, params.NetworkID, params.PeerID, params.StartTime, params.EndTime, params.GroupBy)
	if err != nil {
		timeline = nil
	}

	return &model.StatsTrafficResponse{
		TotalIn:     totalIn,
		TotalOut:    totalOut,
		TopPeers:    topPeers,
		TopNetworks: topNetworks,
		TopMoons:    nil,
		Timeline:    timeline,
	}, nil
}

// Top 获取 Top N 统计
func (s *StatsService) Top(ctx context.Context) (*model.StatsTopResponse, error) {
	now := time.Now()
	startTime := now.Add(-24 * time.Hour)

	// 获取 Top 节点
	topPeers, err := s.auditRepo.GetTopPeersByTraffic(ctx, startTime, now, 10)
	if err != nil {
		topPeers = nil
	}

	// 获取 Top 网络
	topNetworks, err := s.auditRepo.GetTopNetworksByTraffic(ctx, startTime, now, 10)
	if err != nil {
		topNetworks = nil
	}

	return &model.StatsTopResponse{
		TopPeers:    topPeers,
		TopNetworks: topNetworks,
	}, nil
}

// ReceiveTraffic 接收流量上报
func (s *StatsService) ReceiveTraffic(ctx context.Context, req *model.ReceiveTrafficRequest) error {
	peerID, err := uuid.Parse(req.PeerID)
	if err != nil {
		return err
	}

	peer, err := s.peerRepo.GetByID(ctx, peerID)
	if err != nil {
		return err
	}

	sessionID, _ := uuid.Parse(req.SessionID)

	log := &model.TrafficLog{
		PeerID:      peerID,
		SessionID:   &sessionID,
		NetworkID:   peer.NetworkID,
		Direction:   req.Direction,
		Protocol:    req.Protocol,
		Bytes:       req.Bytes,
		Packets:     req.Packets,
		DurationSec: req.Duration,
		TunnelType:  req.TunnelType,
		LoggedAt:    time.Now(),
	}

	return s.auditRepo.LogTraffic(ctx, log)
}

// BroadcastPeerEvent 广播节点上线下线事件
func (s *StatsService) BroadcastPeerEvent(ctx context.Context, peerID uuid.UUID, eventType string) error {
	peer, err := s.peerRepo.GetByID(ctx, peerID)
	if err != nil {
		return err
	}

	_ = map[string]interface{}{
		"type":       "peer_event",
		"event":      eventType,
		"peer_id":    peerID,
		"device_id":  peer.DeviceID,
		"network_id": peer.NetworkID,
		"online":     peer.Online,
		"hostname":   peer.Hostname,
		"timestamp":  time.Now().Unix(),
	}

	// TODO: 通过 WebSocket 广播事件
	return nil
}
