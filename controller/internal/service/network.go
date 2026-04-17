package service

import (
	"context"
	"errors"
	"net"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/repository"
)

var (
	ErrNetworkNotFound       = errors.New("网络不存在")
	ErrNetworkCIDRConflict   = errors.New("网段与现有网络冲突")
	ErrNetworkNoAvailableIP  = errors.New("网段内没有可用 IP")
	ErrInvalidCIDR           = errors.New("无效的 CIDR 格式")
	ErrGatewayIPConflict     = errors.New("网关 IP 与现有节点冲突")
)

// NetworkService 网络服务
type NetworkService struct {
	networkRepo *repository.NetworkRepository
	peerRepo    *repository.PeerRepository
	userRepo    *repository.UserRepository
	moonRepo    *repository.MoonRepository
	auditRepo   *repository.AuditRepository
}

func NewNetworkService(networkRepo *repository.NetworkRepository, peerRepo *repository.PeerRepository, userRepo *repository.UserRepository, moonRepo *repository.MoonRepository, auditRepo *repository.AuditRepository) *NetworkService {
	return &NetworkService{
		networkRepo: networkRepo,
		peerRepo:    peerRepo,
		userRepo:    userRepo,
		moonRepo:    moonRepo,
		auditRepo:   auditRepo,
	}
}

// AllocateCIDR 自动分配网段（IPAM 算法）
func (s *NetworkService) AllocateCIDR(ctx context.Context, networkName string, requiredSize int, operatorID uuid.UUID) (*model.Network, error) {
	// 获取所有现有网络
	networks, _, err := s.networkRepo.List(ctx, &model.PageParams{Limit: 1000})
	if err != nil {
		return nil, err
	}

	// 计算所需的 CIDR 前缀长度
	// requiredSize 是需要的 IP 数量，需要转换为 CIDR 前缀
	// 例如：需要 254 个 IP -> /24 (256 - 2 = 254 可用)
	prefix := 32
	if requiredSize >= 65534 {
		prefix = 16
	} else if requiredSize >= 254 {
		prefix = 24
	} else if requiredSize >= 126 {
		prefix = 25
	} else if requiredSize >= 62 {
		prefix = 26
	} else if requiredSize >= 30 {
		prefix = 27
	} else {
		prefix = 28
	}

	// 使用 10.0.0.0/8 私有地址段分配
	baseIP := net.ParseIP("10.0.0.0")
	if baseIP == nil {
		return nil, ErrInvalidCIDR
	}

	// 遍历寻找可用网段
	for i := 0; i < 256; i++ {
		for j := 0; j < 256; j++ {
			cidr := net.IPNet{
				IP:   net.IP{10, byte(i), byte(j), 0},
				Mask: net.CIDRMask(prefix, 32),
			}

			// 检查是否与现有网络冲突
			conflict := false
			for _, n := range networks {
				existingCIDR, _, err := net.ParseCIDR(n.CIDR)
				if err != nil {
					continue
				}
				existingNet := net.IPNet{
					IP:   existingCIDR,
					Mask: net.CIDRMask(prefix, 32),
				}
				if cidr.Contains(existingNet.IP) || existingNet.Contains(cidr.IP) {
					conflict = true
					break
				}
			}

			if !conflict {
				// 找到可用网段，创建网络
				gatewayIP := net.IP{10, byte(i), byte(j), 1}.String()
				req := &model.NetworkCreateRequest{
					Name:      networkName,
					CIDR:      cidr.String(),
					GatewayIP: gatewayIP,
				}
				return s.Create(ctx, req, operatorID)
			}
		}
	}

	return nil, ErrNetworkNoAvailableIP
}

// CheckCIDRConflict 检测 CIDR 冲突
func (s *NetworkService) CheckCIDRConflict(ctx context.Context, cidr string) (bool, error) {
	var err error
	_, _, err = net.ParseCIDR(cidr)
	if err != nil {
		return false, ErrInvalidCIDR
	}

	// 获取所有现有网络
	networks, _, err := s.networkRepo.List(ctx, &model.PageParams{Limit: 1000})
	if err != nil {
		return false, err
	}

	newIP, newNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false, ErrInvalidCIDR
	}

	for _, n := range networks {
		_, existingNet, err := net.ParseCIDR(n.CIDR)
		if err != nil {
			continue
		}

		// 检查是否重叠
		if newNet.Contains(existingNet.IP) || existingNet.Contains(newIP) {
			return true, nil
		}
	}

	return false, nil
}

// ValidateCIDR 验证 CIDR 格式和范围
func (s *NetworkService) ValidateCIDR(cidr string) error {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return ErrInvalidCIDR
	}

	if !isPrivateIP(ip) {
		return errors.New("仅支持私有地址段（10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16）")
	}

	return nil
}

// isPrivateIP 判断是否为私有 IP
func isPrivateIP(ip net.IP) bool {
	// 检查 10.0.0.0/8
	if ip[0] == 10 {
		return true
	}
	// 检查 172.16.0.0/12
	if ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31 {
		return true
	}
	// 检查 192.168.0.0/16
	if ip[0] == 192 && ip[1] == 168 {
		return true
	}
	return false
}

// GetAvailableIPs 获取网段内可用 IP 列表
func (s *NetworkService) GetAvailableIPs(ctx context.Context, networkID uuid.UUID) ([]string, error) {
	network, err := s.networkRepo.GetByID(ctx, networkID)
	if err != nil {
		return nil, err
	}
	if network == nil {
		return nil, ErrNetworkNotFound
	}

	ip, ipNet, err := net.ParseCIDR(network.CIDR)
	if err != nil {
		return nil, ErrInvalidCIDR
	}

	// 获取网络地址和广播地址
	networkAddr := ip.Mask(ipNet.Mask)
	broadcastAddr := make(net.IP, len(ip))
	for i := range ip {
		broadcastAddr[i] = ip[i] | ^ipNet.Mask[i]
	}

	var availableIPs []string
	for ip := networkAddr.Mask(ipNet.Mask); ip != nil; incIP(ip) {
		ipMasked := ip.To4()
		if ipMasked == nil || len(ipMasked) != 4 {
			break
		}
		// 跳过网络地址
		if ipMasked.Equal(networkAddr) {
			continue
		}
		// 跳过广播地址
		if ipMasked.Equal(broadcastAddr) {
			continue
		}
		// 跳过网关地址
		if ipMasked.String() == network.GatewayIP {
			continue
		}
		availableIPs = append(availableIPs, ipMasked.String())
	}

	return availableIPs, nil
}

// incIP IP 递增
func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

func (s *NetworkService) Create(ctx context.Context, req *model.NetworkCreateRequest, operatorID uuid.UUID) (*model.Network, error) {
	network, err := s.networkRepo.Create(ctx, req, operatorID)
	if err != nil {
		return nil, err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "创建网络",
		ResourceType: "network",
		ResourceID:   &network.ID,
		ResourceName: &network.Name,
	})

	return network, nil
}

func (s *NetworkService) Get(ctx context.Context, id uuid.UUID) (*model.Network, error) {
	network, err := s.networkRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if network == nil {
		return nil, ErrNetworkNotFound
	}
	return network, nil
}

func (s *NetworkService) List(ctx context.Context, params *model.PageParams) ([]model.Network, int64, error) {
	return s.networkRepo.List(ctx, params)
}

func (s *NetworkService) Update(ctx context.Context, id uuid.UUID, req *model.NetworkUpdateRequest, operatorID uuid.UUID) error {
	network, err := s.networkRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if network == nil {
		return ErrNetworkNotFound
	}

	if err := s.networkRepo.Update(ctx, id, req); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "更新网络",
		ResourceType: "network",
		ResourceID:   &id,
		ResourceName: &network.Name,
	})

	return nil
}

func (s *NetworkService) Delete(ctx context.Context, id uuid.UUID, operatorID uuid.UUID) error {
	network, err := s.networkRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if network == nil {
		return ErrNetworkNotFound
	}

	if err := s.networkRepo.Delete(ctx, id); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "删除网络",
		ResourceType: "network",
		ResourceID:   &id,
		ResourceName: &network.Name,
	})

	return nil
}

func (s *NetworkService) ListPeers(ctx context.Context, networkID uuid.UUID) ([]model.Peer, error) {
	return s.networkRepo.ListPeers(ctx, networkID)
}

func (s *NetworkService) RemovePeer(ctx context.Context, networkID, peerID uuid.UUID, operatorID uuid.UUID) error {
	if err := s.peerRepo.DeleteByNetwork(ctx, networkID, peerID); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "移除网络节点",
		ResourceType: "network_peer",
		ResourceID:   &networkID,
	})

	return nil
}

func (s *NetworkService) ListUsers(ctx context.Context, networkID uuid.UUID, params *model.PageParams) ([]model.User, int64, error) {
	return s.networkRepo.ListUsers(ctx, networkID, params)
}

func (s *NetworkService) AssignUser(ctx context.Context, networkID uuid.UUID, req *model.AssignUserRequest, operatorID uuid.UUID) error {
	if err := s.networkRepo.AssignUser(ctx, networkID, req.UserID, req.Role, &req.PolicyID); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "分配用户到网络",
		ResourceType: "network_user",
		ResourceID:   &networkID,
	})

	return nil
}

func (s *NetworkService) UpdateUserPolicy(ctx context.Context, networkID, userID uuid.UUID, req *model.UpdateUserPolicyRequest, operatorID uuid.UUID) error {
	if err := s.networkRepo.AssignUser(ctx, networkID, userID, req.Role, &req.PolicyID); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "更新用户在网络中的策略",
		ResourceType: "network_user",
		ResourceID:   &networkID,
	})

	return nil
}

func (s *NetworkService) RemoveUser(ctx context.Context, networkID, userID uuid.UUID, operatorID uuid.UUID) error {
	if err := s.networkRepo.RemoveUser(ctx, networkID, userID); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "从网络移除用户",
		ResourceType: "network_user",
		ResourceID:   &networkID,
	})

	return nil
}
