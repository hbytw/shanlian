package service

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/crypto"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/repository"
)

// 错误已在 auth.go 中定义，可直接使用

type UserService struct {
	userRepo    *repository.UserRepository
	networkRepo *repository.NetworkRepository
	auditRepo   *repository.AuditRepository
}

func NewUserService(userRepo *repository.UserRepository, networkRepo *repository.NetworkRepository, auditRepo *repository.AuditRepository) *UserService {
	return &UserService{
		userRepo:    userRepo,
		networkRepo: networkRepo,
		auditRepo:   auditRepo,
	}
}

func (s *UserService) Create(ctx context.Context, req *model.UserCreateRequest, operatorID uuid.UUID) (*model.User, error) {
	// 检查用户名是否存在
	existing, err := s.userRepo.GetByUsername(ctx, req.Username)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ErrUsernameExists
	}

	// 检查邮箱是否存在
	existing, err = s.userRepo.GetByEmail(ctx, req.Email)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ErrEmailExists
	}

	// 哈希密码
	hashedPassword, err := crypto.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	user, err := s.userRepo.Create(ctx, req, hashedPassword)
	if err != nil {
		return nil, err
	}

	// 记录日志
	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "创建用户",
		ResourceType: "user",
		ResourceID:   &user.ID,
		ResourceName: &user.Username,
	})

	return user, nil
}

func (s *UserService) Get(ctx context.Context, id uuid.UUID) (*model.User, error) {
	user, err := s.userRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrUserNotFound
	}
	return user, nil
}

func (s *UserService) List(ctx context.Context, params *model.PageParams) (*model.UserListResponse, error) {
	users, total, err := s.userRepo.List(ctx, params)
	if err != nil {
		return nil, err
	}
	return &model.UserListResponse{
		Users:  users,
		Total:  total,
		Page:   params.Page,
		Limit:  params.Limit,
	}, nil
}

func (s *UserService) Update(ctx context.Context, id uuid.UUID, req *model.UserUpdateRequest, operatorID uuid.UUID) error {
	user, err := s.userRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if user == nil {
		return ErrUserNotFound
	}

	if err := s.userRepo.Update(ctx, id, req); err != nil {
		return err
	}

	// 记录日志
	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "更新用户",
		ResourceType: "user",
		ResourceID:   &id,
	})

	return nil
}

func (s *UserService) Delete(ctx context.Context, id uuid.UUID, operatorID uuid.UUID) error {
	user, err := s.userRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if user == nil {
		return ErrUserNotFound
	}

	if err := s.userRepo.Delete(ctx, id); err != nil {
		return err
	}

	// 记录日志
	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "删除用户",
		ResourceType: "user",
		ResourceID:   &id,
		ResourceName: &user.Username,
	})

	return nil
}

func (s *UserService) ResetPassword(ctx context.Context, id uuid.UUID, req *model.ChangePasswordRequest, operatorID uuid.UUID) error {
	user, err := s.userRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if user == nil {
		return ErrUserNotFound
	}

	// 验证旧密码
	if !crypto.CheckPassword(req.OldPassword, user.PasswordHash) {
		return errors.New("原密码错误")
	}

	// 哈希新密码
	hashedPassword, err := crypto.HashPassword(req.NewPassword)
	if err != nil {
		return err
	}

	if err := s.userRepo.UpdatePassword(ctx, id, hashedPassword); err != nil {
		return err
	}

	// 记录日志
	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "重置密码",
		ResourceType: "user",
		ResourceID:   &id,
	})

	return nil
}
