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
	ErrDeviceNotFound    = errors.New("设备不存在")
	ErrDeviceAlreadyBind = errors.New("设备已被绑定")
)

type DeviceService struct {
	deviceRepo *repository.DeviceRepository
	userRepo   *repository.UserRepository
	networkRepo *repository.NetworkRepository
	auditRepo  *repository.AuditRepository
}

func NewDeviceService(deviceRepo *repository.DeviceRepository, userRepo *repository.UserRepository, networkRepo *repository.NetworkRepository, auditRepo *repository.AuditRepository) *DeviceService {
	return &DeviceService{
		deviceRepo:  deviceRepo,
		userRepo:    userRepo,
		networkRepo: networkRepo,
		auditRepo:   auditRepo,
	}
}

func (s *DeviceService) Get(ctx context.Context, id uuid.UUID) (*model.Device, error) {
	device, err := s.deviceRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if device == nil {
		return nil, ErrDeviceNotFound
	}
	return device, nil
}

func (s *DeviceService) List(ctx context.Context, params *model.PageParams) ([]model.Device, int64, error) {
	return s.deviceRepo.List(ctx, params)
}

func (s *DeviceService) Update(ctx context.Context, id uuid.UUID, req *model.Device, operatorID uuid.UUID) error {
	device, err := s.deviceRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if device == nil {
		return ErrDeviceNotFound
	}

	if err := s.deviceRepo.Update(ctx, id, req); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "更新设备",
		ResourceType: "device",
		ResourceID:   &id,
		ResourceName: &device.DeviceID,
	})

	return nil
}

func (s *DeviceService) Bind(ctx context.Context, req *model.DeviceBindRequest, operatorID uuid.UUID) error {
	device, err := s.deviceRepo.GetByDeviceID(ctx, req.DeviceID)
	if err != nil {
		return err
	}
	if device == nil {
		return ErrDeviceNotFound
	}
	if device.UserID != nil {
		return ErrDeviceAlreadyBind
	}

	userUUID, err := uuid.Parse(req.UserID)
	if err != nil {
		return ErrUserNotFound
	}

	if err := s.deviceRepo.Bind(ctx, req.DeviceID, userUUID); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "绑定设备",
		ResourceType: "device",
		ResourceID:   &device.ID,
		ResourceName: &device.DeviceID,
	})

	return nil
}

func (s *DeviceService) Unbind(ctx context.Context, req *model.DeviceUnbindRequest, operatorID uuid.UUID) error {
	device, err := s.deviceRepo.GetByDeviceID(ctx, req.DeviceID)
	if err != nil {
		return err
	}
	if device == nil {
		return ErrDeviceNotFound
	}

	if err := s.deviceRepo.Unbind(ctx, req.DeviceID); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "解绑设备",
		ResourceType: "device",
		ResourceID:   &device.ID,
		ResourceName: &device.DeviceID,
	})

	return nil
}

func (s *DeviceService) Transfer(ctx context.Context, req *model.DeviceTransferRequest, operatorID uuid.UUID) error {
	device, err := s.deviceRepo.GetByDeviceID(ctx, req.DeviceID)
	if err != nil {
		return err
	}
	if device == nil {
		return ErrDeviceNotFound
	}

	fromUserUUID, err := uuid.Parse(req.FromUser)
	if err != nil {
		return ErrUserNotFound
	}
	toUserUUID, err := uuid.Parse(req.ToUser)
	if err != nil {
		return ErrUserNotFound
	}

	// 解绑原用户
	if err := s.deviceRepo.Unbind(ctx, req.DeviceID); err != nil {
		return err
	}

	// 绑定新用户
	if err := s.deviceRepo.Bind(ctx, req.DeviceID, toUserUUID); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "转移设备",
		ResourceType: "device",
		ResourceID:   &device.ID,
		ResourceName: &device.DeviceID,
		Detail:       []byte(`{"from_user":"` + fromUserUUID.String() + `","to_user":"` + toUserUUID.String() + `"}`),
	})

	return nil
}

// UpdateLastSeen 更新设备最后在线时间
func (s *DeviceService) UpdateLastSeen(ctx context.Context, deviceID string) error {
	return s.deviceRepo.UpdateLastSeen(ctx, deviceID)
}

// SetOffline 设置设备离线
func (s *DeviceService) SetOffline(ctx context.Context, deviceID string) error {
	return s.deviceRepo.SetOffline(ctx, deviceID)
}

// RegisterOrUpdateDevice 注册或更新设备
func (s *DeviceService) RegisterOrUpdateDevice(ctx context.Context, deviceID string, hostname, publicIP, natType, osType, osVersion, clientVersion string) (*model.Device, error) {
	device, err := s.deviceRepo.GetByDeviceID(ctx, deviceID)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	if device == nil {
		// 创建新设备
		device = &model.Device{
			ID:            uuid.New(),
			DeviceID:      deviceID,
			Hostname:      &hostname,
			PublicIP:      &publicIP,
			NATType:       natType,
			Online:        true,
			ClientVersion: &clientVersion,
			OSType:        &osType,
			OSVersion:     &osVersion,
			FirstSeenAt:   now,
			LastSeenAt:    now,
			Enabled:       true,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if err := s.deviceRepo.Create(ctx, device); err != nil {
			return nil, err
		}
	} else {
		// 更新现有设备
		device.Hostname = &hostname
		device.PublicIP = &publicIP
		device.NATType = natType
		device.Online = true
		device.LastSeenAt = now
		device.UpdatedAt = now
		if clientVersion != "" {
			device.ClientVersion = &clientVersion
		}
		if osType != "" {
			device.OSType = &osType
		}
		if osVersion != "" {
			device.OSVersion = &osVersion
		}
		if err := s.deviceRepo.Update(ctx, device.ID, device); err != nil {
			return nil, err
		}
	}

	return device, nil
}
