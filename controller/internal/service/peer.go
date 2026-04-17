package service

import (
	"context"
	"encoding/json"
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

	// 获取节点所属的网络
	network, err := s.networkRepo.GetByID(ctx, peer.NetworkID)
	if err != nil {
		return err
	}
	if network == nil {
		return errors.New("节点所属网络不存在")
	}

	// 从网络网段分配新的 IP
	newIP, err := s.networkRepo.AllocatePeerIP(ctx, peer.NetworkID, id)
	if err != nil {
		return errors.New("分配新 IP 失败：" + err.Error())
	}

	// 更新节点的 IP
	if err := s.peerRepo.UpdateAssignedIP(ctx, id, newIP); err != nil {
		return errors.New("更新节点 IP 失败：" + err.Error())
	}

	detail, _ := json.Marshal(map[string]string{"old_ip": peer.AssignedIP, "new_ip": newIP})
	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "重置节点 IP",
		ResourceType: "peer",
		ResourceID:   &id,
		Detail:       detail,
	})

	return nil
}

// Register 节点注册
func (s *PeerService) Register(ctx context.Context, req *model.PeerRegisterRequest) (*model.PeerRegisterResponse, error) {
	deviceID, err := uuid.Parse(req.DeviceID)
	if err != nil {
		return nil, err
	}
	networkID, err := uuid.Parse(req.NetworkID)
	if err != nil {
		return nil, err
	}

	// 验证设备是否绑定到该网络
	device, err := s.deviceRepo.GetByID(ctx, deviceID)
	if err != nil || device == nil {
		return nil, errors.New("设备不存在")
	}

	// 分配 IP（从 IP 池分配，而非使用请求中的 IP）
	assignedIP, err := s.networkRepo.AllocatePeerIP(ctx, networkID, deviceID)
	if err != nil {
		return nil, errors.New("分配 IP 失败：" + err.Error())
	}

	peer := &model.Peer{
		ID:          uuid.New(),
		DeviceID:   deviceID,
		NetworkID:   networkID,
		PublicIP:    &req.PublicIP,
		PublicPort:  &req.PublicPort,
		NATType:     req.NATType,
		Online:      true,
		AssignedIP:  assignedIP,
		Hostname:    &req.Hostname,
		TunnelType:  req.TunnelType,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		LastSeenAt:  &time.Time{},
	}

	if err := s.peerRepo.Create(ctx, peer); err != nil {
		return nil, err
	}

	// 获取网络信息
	network, err := s.networkRepo.GetByID(ctx, networkID)
	if err != nil {
		return nil, errors.New("获取网络信息失败")
	}

	networkInfo := &model.NetworkInfo{
		ID:        network.ID.String(),
		Name:      network.Name,
		CIDR:      network.CIDR,
		GatewayIP: network.GatewayIP,
	}

	// TODO: 获取策略列表（需要 PolicyRepository）
	policyList := []model.PolicyInfo{}

	// 获取 Moon 列表
	moons, _, err := s.moonRepo.List(ctx, &model.PageParams{Limit: 100})
	if err != nil {
		moons = []model.MoonNode{}
	}
	var moonList []model.MoonInfo
	for _, m := range moons {
		if m.Online {
			moonList = append(moonList, model.MoonInfo{
				ID:        m.ID.String(),
				Domain:    m.Domain,
				PublicIP:  m.PublicIP,
				RelayPort: m.RelayPort,
				WSSPort:   m.WSSPort,
				Weight:    m.Weight,
				Online:    m.Online,
			})
		}
	}

	return &model.PeerRegisterResponse{
		PeerID:      peer.ID.String(),
		AssignedIP:  assignedIP,
		NetworkInfo: networkInfo,
		PolicyList:  policyList,
		MoonList:    moonList,
	}, nil
}

// Heartbeat 节点心跳
func (s *PeerService) Heartbeat(ctx context.Context, req *model.PeerHeartbeatRequest) (*model.PeerHeartbeatResponse, error) {
	id, err := uuid.Parse(req.PeerID)
	if err != nil {
		return nil, err
	}

	peer, err := s.peerRepo.GetByID(ctx, id)
	if err != nil || peer == nil {
		return nil, ErrPeerNotFound
	}

	// 更新在线状态
	if err := s.peerRepo.SetOnline(ctx, id); err != nil {
		return nil, err
	}

	// 获取更新后的 Moon 列表
	moons, _, err := s.moonRepo.List(ctx, &model.PageParams{Limit: 100})
	if err != nil {
		moons = []model.MoonNode{}
	}
	var moonList []model.MoonInfo
	for _, m := range moons {
		if m.Online {
			moonList = append(moonList, model.MoonInfo{
				ID:        m.ID.String(),
				Domain:    m.Domain,
				PublicIP:  m.PublicIP,
				RelayPort: m.RelayPort,
				WSSPort:   m.WSSPort,
				Weight:    m.Weight,
				Online:    m.Online,
			})
		}
	}

	// TODO: 获取策略更新
	policyUpdates := []model.PolicyInfo{}

	return &model.PeerHeartbeatResponse{
		Status:        "ok",
		MoonList:     moonList,
		PolicyUpdates: policyUpdates,
	}, nil
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
