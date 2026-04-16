package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/repository"
)

var (
	ErrPeerNotFound = errors.New("节点不存在")
)

type PeerService struct {
	peerRepo   *repository.PeerRepository
	deviceRepo *repository.DeviceRepository
	networkRepo *repository.NetworkRepository
	moonRepo   *repository.MoonRepository
	auditRepo  *repository.AuditRepository
}

func NewPeerService(peerRepo *repository.PeerRepository, deviceRepo *repository.DeviceRepository, networkRepo *repository.NetworkRepository, moonRepo *repository.MoonRepository, auditRepo *repository.AuditRepository) *PeerService {
	return &PeerService{
		peerRepo:   peerRepo,
		deviceRepo: deviceRepo,
		networkRepo: networkRepo,
		moonRepo:   moonRepo,
		auditRepo:  auditRepo,
	}
}

func (s *PeerService) Get(ctx context.Context, id uuid.UUID) (*model.Peer, error) {
	peer, err := s.peerRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if peer == nil {
		return nil, ErrPeerNotFound
	}
	return peer, nil
}

func (s *PeerService) List(ctx context.Context, params *model.PageParams) ([]model.Peer, int64, error) {
	return s.peerRepo.List(ctx, params)
}

func (s *PeerService) Disconnect(ctx context.Context, id uuid.UUID, operatorID uuid.UUID) error {
	peer, err := s.peerRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if peer == nil {
		return ErrPeerNotFound
	}

	if err := s.peerRepo.SetOffline(ctx, id); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "断开节点连接",
		ResourceType: "peer",
		ResourceID:   &id,
	})

	return nil
}

func (s *PeerService) ResetIP(ctx context.Context, id uuid.UUID, operatorID uuid.UUID) error {
	peer, err := s.peerRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if peer == nil {
		return ErrPeerNotFound
	}

	// TODO: 重新分配 IP
	_ = peer

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "重置节点 IP",
		ResourceType: "peer",
		ResourceID:   &id,
	})

	return nil
}

// Register 节点注册
func (s *PeerService) Register(ctx context.Context, req *model.PeerRegisterRequest) (*model.Peer, error) {
	networkID, err := uuid.Parse(req.NetworkID)
	if err != nil {
		return nil, err
	}

	peer := &model.Peer{
		ID:          uuid.New(),
		NetworkID:   networkID,
		PublicIP:    &req.PublicIP,
		PublicPort:  &req.PublicPort,
		NATType:     req.NATType,
		Online:      true,
		AssignedIP:  req.AssignedIP,
		Hostname:    &req.Hostname,
		TunnelType:  req.TunnelType,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		LastSeenAt:  &time.Time{},
	}

	if err := s.peerRepo.Create(ctx, peer); err != nil {
		return nil, err
	}

	return peer, nil
}

// Heartbeat 节点心跳
func (s *PeerService) Heartbeat(ctx context.Context, peerID string) error {
	id, err := uuid.Parse(peerID)
	if err != nil {
		return err
	}
	return s.peerRepo.SetOnline(ctx, id)
}

// Discover 发现节点
func (s *PeerService) Discover(ctx context.Context, req *model.PeerDiscoverRequest) (*model.PeerDiscoverResponse, error) {
	networkID, err := uuid.Parse(req.NetworkID)
	if err != nil {
		return nil, err
	}

	peers, err := s.peerRepo.GetNetworkPeers(ctx, networkID)
	if err != nil {
		return nil, err
	}

	var peerInfos []model.PeerInfo
	for _, p := range peers {
		if p.Online {
			peerInfos = append(peerInfos, model.PeerInfo{
				PeerID:     p.ID.String(),
				PublicIP:   *p.PublicIP,
				PublicPort: *p.PublicPort,
				NATType:    p.NATType,
				Online:     p.Online,
			})
		}
	}

	return &model.PeerDiscoverResponse{Peers: peerInfos}, nil
}

// SetOffline 设置节点离线
func (s *PeerService) SetOffline(ctx context.Context, id uuid.UUID) error {
	return s.peerRepo.SetOffline(ctx, id)
}
