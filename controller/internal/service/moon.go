package service

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/repository"
)

var (
	ErrMoonNotFound       = errors.New("Moon 节点不存在")
	ErrMoonDuplicateDomain = errors.New("Moon 域名已存在")
)

type MoonService struct {
	moonRepo  *repository.MoonRepository
	auditRepo *repository.AuditRepository
}

func NewMoonService(moonRepo *repository.MoonRepository, auditRepo *repository.AuditRepository) *MoonService {
	return &MoonService{
		moonRepo:  moonRepo,
		auditRepo: auditRepo,
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
func (s *MoonService) Register(ctx context.Context, req *model.MoonRegisterRequest) (*model.MoonNode, error) {
	// 检查域名是否已存在
	existing, err := s.moonRepo.GetByDomain(ctx, req.Domain)
	if err != nil {
		return nil, err
	}

	if existing != nil {
		// 更新现有 Moon
		if err := s.moonRepo.UpdateHeartbeat(ctx, req.Domain, req.Version); err != nil {
			return nil, err
		}
		return existing, nil
	}

	// 创建新 Moon
	createReq := &model.MoonCreateRequest{
		Domain:    req.Domain,
		PublicIP:  req.PublicIP,
		GatewayIP: req.Domain, // 使用域名作为网关
		UDPPort:   req.UDPPort,
	}

	return s.moonRepo.Create(ctx, createReq)
}

// Heartbeat Moon 心跳
func (s *MoonService) Heartbeat(ctx context.Context, req *model.MoonHeartbeatRequest) error {
	return s.moonRepo.UpdateHeartbeat(ctx, req.Domain, req.Status)
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

	_ = map[string]interface{}{
		"type":       "moon_event",
		"event":      eventType,
		"moon_id":    moonID,
		"domain":     moon.Domain,
		"public_ip":  moon.PublicIP,
		"online":     moon.Online,
		"timestamp":  time.Now().Unix(),
	}

	// TODO: 通过 WebSocket 广播事件
	log.Printf("Moon 事件广播：%s, moon=%s, event=%s", eventType, moon.Domain, moon.PublicIP)
	return nil
}
