package service

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/repository"
	"github.com/hbytw/shanlian/controller/pkg/event"
)

var (
	ErrMoonNotFound       = errors.New("Moon 节点不存在")
	ErrMoonDuplicateDomain = errors.New("Moon 域名已存在")
)

// MoonService Moon 服务
type MoonService struct {
	moonRepo  *repository.MoonRepository
	auditRepo *repository.AuditRepository

	// 心跳超时检测
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// NewMoonService 创建 Moon 服务
func NewMoonService(moonRepo *repository.MoonRepository, auditRepo *repository.AuditRepository) *MoonService {
	s := &MoonService{
		moonRepo:  moonRepo,
		auditRepo: auditRepo,
		stopChan:  make(chan struct{}),
	}
	// 启动心跳超时检测
	s.wg.Add(1)
	go s.checkHeartbeatTimeout()
	return s
}

// Stop 停止服务
func (s *MoonService) Stop() {
	close(s.stopChan)
	s.wg.Wait()
}

// checkHeartbeatTimeout 定期检查 Moon 心跳超时
func (s *MoonService) checkHeartbeatTimeout() {
	defer s.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.markTimeoutMoons()
		}
	}
}

// markTimeoutMoons 标记超时的 Moon 为离线
func (s *MoonService) markTimeoutMoons() {
	ctx := context.Background()
	timeout := time.Now().Add(-90 * time.Second) // 90秒超时

	moons, err := s.moonRepo.GetMoonsLastHeartbeatBefore(ctx, timeout)
	if err != nil {
		log.Printf("检查 Moon 超时失败：%v", err)
		return
	}

	for _, moon := range moons {
		if err := s.moonRepo.SetOffline(ctx, moon.Domain); err != nil {
			log.Printf("设置 Moon 离线失败：%s, error: %v", moon.Domain, err)
			continue
		}
		log.Printf("Moon 超时离线：%s (最后心跳: %v)", moon.Domain, moon.LastHeartbeatAt)

		// 广播离线事件
		s.BroadcastMoonEvent(ctx, moon.ID, "offline")
	}
}

func (s *MoonService) Create(ctx context.Context, req *model.MoonCreateRequest, operatorID uuid.UUID) (*model.MoonNode, error) {
	// 检查域名是否已存在
	existing, err := s.moonRepo.GetByDomain(ctx, req.Domain)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ErrMoonDuplicateDomain
	}

	moon, err := s.moonRepo.Create(ctx, req)
	if err != nil {
		return nil, err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "创建 Moon 节点",
		ResourceType: "moon",
		ResourceID:   &moon.ID,
		ResourceName: &moon.Domain,
	})

	return moon, nil
}

func (s *MoonService) Get(ctx context.Context, id uuid.UUID) (*model.MoonNode, error) {
	moon, err := s.moonRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if moon == nil {
		return nil, ErrMoonNotFound
	}
	return moon, nil
}

func (s *MoonService) List(ctx context.Context, params *model.PageParams) ([]model.MoonNode, int64, error) {
	return s.moonRepo.List(ctx, params)
}

func (s *MoonService) Update(ctx context.Context, id uuid.UUID, req *model.MoonUpdateRequest, operatorID uuid.UUID) error {
	moon, err := s.moonRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if moon == nil {
		return ErrMoonNotFound
	}

	if err := s.moonRepo.Update(ctx, id, req); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "更新 Moon 节点",
		ResourceType: "moon",
		ResourceID:   &id,
		ResourceName: &moon.Domain,
	})

	return nil
}

func (s *MoonService) Delete(ctx context.Context, id uuid.UUID, operatorID uuid.UUID) error {
	moon, err := s.moonRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if moon == nil {
		return ErrMoonNotFound
	}

	if err := s.moonRepo.Delete(ctx, id); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "删除 Moon 节点",
		ResourceType: "moon",
		ResourceID:   &id,
		ResourceName: &moon.Domain,
	})

	return nil
}

// Register Moon 节点注册
func (s *MoonService) Register(ctx context.Context, req *model.MoonRegisterRequest) (*model.MoonRegisterResponse, error) {
	// 检查域名是否已存在
	existing, err := s.moonRepo.GetByDomain(ctx, req.Domain)
	if err != nil {
		return nil, err
	}

	if existing != nil {
		// 更新现有 Moon（再次注册时只更新心跳）
		if err := s.moonRepo.UpdateHeartbeat(ctx, req.Domain, req.Version); err != nil {
			return nil, err
		}
		// 重新注册时仍返回 api_secret（MoonRegisterResponse.APISecret 有 json:"api_secret" tag）
		return &model.MoonRegisterResponse{
			ID:         existing.ID,
			Domain:     existing.Domain,
			PublicIP:   existing.PublicIP,
			GatewayIP:  existing.GatewayIP,
			RelayPort:  existing.RelayPort,
			WSSPort:    existing.WSSPort,
			UDPPort:    existing.UDPPort,
			HealthPort: existing.HealthPort,
			Weight:     existing.Weight,
			APIKey:     existing.APIKey,
			APISecret:  existing.APISecret, // 重新注册也返回 secret
			Online:     existing.Online,
			Version:    existing.Version,
		}, nil
	}

	// 创建新 Moon（首次注册返回完整信息包括 api_secret）
	createReq := &model.MoonCreateRequest{
		Domain:     req.Domain,
		PublicIP:   req.PublicIP,
		GatewayIP:  req.GatewayIP,
		UDPPort:    req.UDPPort,
		RelayPort:  req.RelayPort,
		WSSPort:    req.WSSPort,
	}

	moon, err := s.moonRepo.Create(ctx, createReq)
	if err != nil {
		return nil, err
	}

	// 首次注册返回完整信息
	return &model.MoonRegisterResponse{
		ID:          moon.ID,
		Domain:      moon.Domain,
		PublicIP:    moon.PublicIP,
		GatewayIP:   moon.GatewayIP,
		RelayPort:   moon.RelayPort,
		WSSPort:     moon.WSSPort,
		UDPPort:     moon.UDPPort,
		HealthPort:  moon.HealthPort,
		Weight:      moon.Weight,
		APIKey:      moon.APIKey,
		APISecret:   moon.APISecret, // 首次注册返回 secret
		Online:      moon.Online,
		Version:     moon.Version,
	}, nil
}

// Heartbeat Moon 心跳
func (s *MoonService) Heartbeat(ctx context.Context, req *model.MoonHeartbeatRequest) (*model.MoonHeartbeatResponse, error) {
	// 更新心跳
	if err := s.moonRepo.UpdateHeartbeat(ctx, req.Domain, req.Status); err != nil {
		return nil, err
	}

	// 更新连接数
	moon, err := s.moonRepo.GetByDomain(ctx, req.Domain)
	if err != nil {
		return nil, err
	}
	if moon != nil {
		s.moonRepo.UpdateConnections(ctx, moon.ID, req.Connections)
	}

	// 获取在线 Moon 数量
	moonCount, err := s.moonRepo.GetOnlineMoonCount(ctx)
	if err != nil {
		moonCount = 0
	}

	return &model.MoonHeartbeatResponse{
		Status:    "ok",
		MoonCount: moonCount,
		Shutdown:  false, // TODO: 实现关闭指令逻辑
	}, nil
}

// AssignGatewayIP 为 Moon 分配网关 IP
func (s *MoonService) AssignGatewayIP(ctx context.Context, moonID uuid.UUID, networkID uuid.UUID) (string, error) {
	moon, err := s.moonRepo.GetByID(ctx, moonID)
	if err != nil {
		return "", err
	}
	if moon == nil {
		return "", ErrMoonNotFound
	}

	// 从网络获取可用 IP
	network, err := s.moonRepo.GetNetworkByID(ctx, networkID)
	if err != nil {
		return "", err
	}
	if network == nil {
		return "", errors.New("网络不存在")
	}

	// 从网段中分配一个 IP 作为网关
	assignedIP, err := s.moonRepo.AllocateMoonIP(ctx, networkID, moonID)
	if err != nil {
		return "", errors.New("分配网关 IP 失败：" + err.Error())
	}

	// 更新 Moon 的网关 IP
	updateReq := &model.MoonUpdateRequest{
		GatewayIP: assignedIP,
	}
	if err := s.moonRepo.Update(ctx, moonID, updateReq); err != nil {
		return "", err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   uuid.Nil,
		Action:       "分配网关 IP",
		ResourceType: "moon",
		ResourceID:   &moonID,
		ResourceName: &moon.Domain,
	})

	return assignedIP, nil
}

// ReleaseGatewayIP 释放 Moon 的网关 IP
func (s *MoonService) ReleaseGatewayIP(ctx context.Context, moonID uuid.UUID) error {
	moon, err := s.moonRepo.GetByID(ctx, moonID)
	if err != nil {
		return err
	}
	if moon == nil {
		return ErrMoonNotFound
	}

	// 释放分配的 IP
	if err := s.moonRepo.ReleaseMoonIP(ctx, moon.ID); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   uuid.Nil,
		Action:       "释放网关 IP",
		ResourceType: "moon",
		ResourceID:   &moonID,
		ResourceName: &moon.Domain,
	})

	return nil
}

// GetMoonGatewayIP 获取 Moon 的网关 IP
func (s *MoonService) GetMoonGatewayIP(ctx context.Context, moonID uuid.UUID) (string, error) {
	moon, err := s.moonRepo.GetByID(ctx, moonID)
	if err != nil {
		return "", err
	}
	if moon == nil {
		return "", ErrMoonNotFound
	}
	return moon.GatewayIP, nil
}

// BroadcastMoonEvent 广播 Moon 上线/下线事件
func (s *MoonService) BroadcastMoonEvent(ctx context.Context, moonID uuid.UUID, eventType string) error {
	moon, err := s.moonRepo.GetByID(ctx, moonID)
	if err != nil {
		return err
	}

	evtType := event.EventMoonOnline
	if eventType == "offline" {
		evtType = event.EventMoonOffline
	}

	evt := &event.Event{
		Type:      evtType,
		Timestamp: time.Now().Unix(),
		Data: map[string]interface{}{
			"moon_id":   moonID,
			"domain":    moon.Domain,
			"public_ip": moon.PublicIP,
			"online":    moon.Online,
		},
	}

	event.GetEventBus().Publish(evt)
	log.Printf("Moon 事件广播：%s, moon=%s, event=%s", eventType, moon.Domain, moon.PublicIP)
	return nil
}
