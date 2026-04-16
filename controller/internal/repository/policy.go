package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
)

type PolicyRepository struct {
	db *PostgresDB
}

func NewPolicyRepository(db *PostgresDB) *PolicyRepository {
	return &PolicyRepository{db: db}
}

func (r *PolicyRepository) Create(ctx context.Context, req *model.PolicyCreateRequest, createdBy uuid.UUID) (*model.Policy, error) {
	policy := &model.Policy{
		ID:         uuid.New(),
		NetworkID:  req.NetworkID,
		Name:       req.Name,
		PolicyType: req.PolicyType,
		Config:     req.Config,
		Priority:   req.Priority,
		Enabled:    true,
		CreatedBy:  &createdBy,
	}
	if req.Enabled != nil {
		policy.Enabled = *req.Enabled
	}

	query := `
		INSERT INTO policies (id, network_id, name, policy_type, config, priority, enabled, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
	`
	_, err := r.db.ExecContext(ctx, query,
		policy.ID, policy.NetworkID, policy.Name, policy.PolicyType,
		policy.Config, policy.Priority, policy.Enabled, policy.CreatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("创建策略失败：%w", err)
	}
	return policy, nil
}

func (r *PolicyRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.Policy, error) {
	query := `
		SELECT id, network_id, name, policy_type, config, priority, enabled, created_at, updated_at, created_by
		FROM policies WHERE id = $1
	`
	policy := &model.Policy{}
	var configBytes []byte
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&policy.ID, &policy.NetworkID, &policy.Name, &policy.PolicyType,
		&configBytes, &policy.Priority, &policy.Enabled,
		&policy.CreatedAt, &policy.UpdatedAt, &policy.CreatedBy,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询策略失败：%w", err)
	}
	policy.Config = json.RawMessage(configBytes)
	return policy, nil
}

func (r *PolicyRepository) List(ctx context.Context, params *model.PageParams) ([]model.Policy, int64, error) {
	countQuery := `SELECT COUNT(*) FROM policies WHERE 1=1`
	listQuery := `
		SELECT id, network_id, name, policy_type, config, priority, enabled, created_at, updated_at, created_by
		FROM policies WHERE 1=1
	`

	args := []interface{}{}
	argIdx := 1

	if params.Search != "" {
		searchPattern := "%" + params.Search + "%"
		countQuery += fmt.Sprintf(" AND name LIKE $%d", argIdx)
		listQuery += fmt.Sprintf(" AND name LIKE $%d", argIdx)
		args = append(args, searchPattern)
		argIdx++
	}

	var total int64
	err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("统计策略数量失败：%w", err)
	}

	listQuery += fmt.Sprintf(" ORDER BY priority DESC, created_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, params.Limit, params.GetOffset())

	rows, err := r.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询策略列表失败：%w", err)
	}
	defer rows.Close()

	var policies []model.Policy
	for rows.Next() {
		var policy model.Policy
		var configBytes []byte
		err := rows.Scan(
			&policy.ID, &policy.NetworkID, &policy.Name, &policy.PolicyType,
			&configBytes, &policy.Priority, &policy.Enabled,
			&policy.CreatedAt, &policy.UpdatedAt, &policy.CreatedBy,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("扫描策略行失败：%w", err)
		}
		policy.Config = json.RawMessage(configBytes)
		policies = append(policies, policy)
	}

	return policies, total, nil
}

func (r *PolicyRepository) Update(ctx context.Context, id uuid.UUID, req *model.PolicyUpdateRequest) error {
	query := `UPDATE policies SET name = $1, config = $2, priority = $3, enabled = $4, updated_at = NOW() WHERE id = $5`
	_, err := r.db.ExecContext(ctx, query, req.Name, req.Config, req.Priority, req.Enabled, id)
	if err != nil {
		return fmt.Errorf("更新策略失败：%w", err)
	}
	return nil
}

func (r *PolicyRepository) UpdateStatus(ctx context.Context, id uuid.UUID, enabled bool) error {
	query := `UPDATE policies SET enabled = $1, updated_at = NOW() WHERE id = $2`
	_, err := r.db.ExecContext(ctx, query, enabled, id)
	if err != nil {
		return fmt.Errorf("更新策略状态失败：%w", err)
	}
	return nil
}

func (r *PolicyRepository) Delete(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM policies WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("删除策略失败：%w", err)
	}
	return nil
}

func (r *PolicyRepository) GetByNetwork(ctx context.Context, networkID uuid.UUID) ([]model.Policy, error) {
	query := `
		SELECT id, network_id, name, policy_type, config, priority, enabled, created_at, updated_at, created_by
		FROM policies WHERE network_id = $1 ORDER BY priority DESC, created_at ASC
	`
	rows, err := r.db.QueryContext(ctx, query, networkID)
	if err != nil {
		return nil, fmt.Errorf("查询网络策略失败：%w", err)
	}
	defer rows.Close()

	var policies []model.Policy
	for rows.Next() {
		var policy model.Policy
		var configBytes []byte
		err := rows.Scan(
			&policy.ID, &policy.NetworkID, &policy.Name, &policy.PolicyType,
			&configBytes, &policy.Priority, &policy.Enabled,
			&policy.CreatedAt, &policy.UpdatedAt, &policy.CreatedBy,
		)
		if err != nil {
			return nil, fmt.Errorf("扫描策略行失败：%w", err)
		}
		policy.Config = json.RawMessage(configBytes)
		policies = append(policies, policy)
	}
	return policies, nil
}
