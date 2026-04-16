package service

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/repository"
)

var (
	ErrPolicyNotFound = errors.New("策略不存在")
)

type PolicyService struct {
	policyRepo  *repository.PolicyRepository
	networkRepo *repository.NetworkRepository
	auditRepo   *repository.AuditRepository
}

func NewPolicyService(policyRepo *repository.PolicyRepository, networkRepo *repository.NetworkRepository, auditRepo *repository.AuditRepository) *PolicyService {
	return &PolicyService{
		policyRepo:  policyRepo,
		networkRepo: networkRepo,
		auditRepo:   auditRepo,
	}
}

func (s *PolicyService) Create(ctx context.Context, req *model.PolicyCreateRequest, operatorID uuid.UUID) (*model.Policy, error) {
	policy, err := s.policyRepo.Create(ctx, req, operatorID)
	if err != nil {
		return nil, err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "创建策略",
		ResourceType: "policy",
		ResourceID:   &policy.ID,
		ResourceName: &policy.Name,
	})

	return policy, nil
}

func (s *PolicyService) Get(ctx context.Context, id uuid.UUID) (*model.Policy, error) {
	policy, err := s.policyRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if policy == nil {
		return nil, ErrPolicyNotFound
	}
	return policy, nil
}

func (s *PolicyService) List(ctx context.Context, params *model.PageParams) ([]model.Policy, int64, error) {
	return s.policyRepo.List(ctx, params)
}

func (s *PolicyService) Update(ctx context.Context, id uuid.UUID, req *model.PolicyUpdateRequest, operatorID uuid.UUID) error {
	policy, err := s.policyRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if policy == nil {
		return ErrPolicyNotFound
	}

	if err := s.policyRepo.Update(ctx, id, req); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "更新策略",
		ResourceType: "policy",
		ResourceID:   &id,
		ResourceName: &policy.Name,
	})

	return nil
}

func (s *PolicyService) Delete(ctx context.Context, id uuid.UUID, operatorID uuid.UUID) error {
	policy, err := s.policyRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if policy == nil {
		return ErrPolicyNotFound
	}

	if err := s.policyRepo.Delete(ctx, id); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "删除策略",
		ResourceType: "policy",
		ResourceID:   &id,
		ResourceName: &policy.Name,
	})

	return nil
}

func (s *PolicyService) UpdateStatus(ctx context.Context, id uuid.UUID, req *model.PolicyStatusRequest, operatorID uuid.UUID) error {
	policy, err := s.policyRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if policy == nil {
		return ErrPolicyNotFound
	}

	if err := s.policyRepo.UpdateStatus(ctx, id, req.Enabled); err != nil {
		return err
	}

	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   operatorID,
		Action:       "更新策略状态",
		ResourceType: "policy",
		ResourceID:   &id,
		ResourceName: &policy.Name,
	})

	return nil
}
