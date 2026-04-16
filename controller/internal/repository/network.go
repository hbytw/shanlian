package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
)

type NetworkRepository struct {
	db *PostgresDB
}

func NewNetworkRepository(db *PostgresDB) *NetworkRepository {
	return &NetworkRepository{db: db}
}

func (r *NetworkRepository) Create(ctx context.Context, req *model.NetworkCreateRequest, createdBy uuid.UUID) (*model.Network, error) {
	network := &model.Network{
		ID:        uuid.New(),
		Name:      req.Name,
		CIDR:      req.CIDR,
		GatewayIP: req.GatewayIP,
		Enabled:   true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		CreatedBy: &createdBy,
	}
	if req.Description != "" {
		network.Description = &req.Description
	}
	if req.Enabled != nil {
		network.Enabled = *req.Enabled
	}

	query := `
		INSERT INTO networks (id, name, cidr, gateway_ip, description, enabled, created_at, updated_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := r.db.ExecContext(ctx, query,
		network.ID, network.Name, network.CIDR, network.GatewayIP,
		network.Description, network.Enabled, network.CreatedAt, network.UpdatedAt, network.CreatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("创建网络失败：%w", err)
	}
	return network, nil
}

func (r *NetworkRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.Network, error) {
	query := `
		SELECT id, name, cidr, gateway_ip, description, enabled, created_at, updated_at, created_by
		FROM networks WHERE id = $1
	`
	network := &model.Network{}
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&network.ID, &network.Name, &network.CIDR, &network.GatewayIP,
		&network.Description, &network.Enabled, &network.CreatedAt, &network.UpdatedAt, &network.CreatedBy,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询网络失败：%w", err)
	}
	return network, nil
}

func (r *NetworkRepository) List(ctx context.Context, params *model.PageParams) ([]model.Network, int64, error) {
	countQuery := `SELECT COUNT(*) FROM networks WHERE 1=1`
	listQuery := `
		SELECT id, name, cidr, gateway_ip, description, enabled, created_at, updated_at, created_by
		FROM networks WHERE 1=1
	`

	args := []interface{}{}
	argIdx := 1

	if params.Search != "" {
		searchPattern := "%" + params.Search + "%"
		countQuery += fmt.Sprintf(" AND (name LIKE $%d OR cidr LIKE $%d)", argIdx, argIdx)
		listQuery += fmt.Sprintf(" AND (name LIKE $%d OR cidr LIKE $%d)", argIdx, argIdx)
		args = append(args, searchPattern)
		argIdx++
	}

	var total int64
	err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("统计网络数量失败：%w", err)
	}

	listQuery += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, params.Limit, params.GetOffset())

	rows, err := r.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询网络列表失败：%w", err)
	}
	defer rows.Close()

	var networks []model.Network
	for rows.Next() {
		var network model.Network
		err := rows.Scan(
			&network.ID, &network.Name, &network.CIDR, &network.GatewayIP,
			&network.Description, &network.Enabled, &network.CreatedAt, &network.UpdatedAt, &network.CreatedBy,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("扫描网络行失败：%w", err)
		}
		networks = append(networks, network)
	}

	return networks, total, nil
}

func (r *NetworkRepository) Update(ctx context.Context, id uuid.UUID, req *model.NetworkUpdateRequest) error {
	query := `UPDATE networks SET name = $1, description = $2, enabled = $3, updated_at = NOW() WHERE id = $4`
	_, err := r.db.ExecContext(ctx, query, req.Name, req.Description, req.Enabled, id)
	if err != nil {
		return fmt.Errorf("更新网络失败：%w", err)
	}
	return nil
}

func (r *NetworkRepository) Delete(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM networks WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("删除网络失败：%w", err)
	}
	return nil
}

// AssignUser 分配用户到网络
func (r *NetworkRepository) AssignUser(ctx context.Context, networkID, userID uuid.UUID, role string, policyID *uuid.UUID) error {
	query := `
		INSERT INTO user_networks (user_id, network_id, role, policy_id, created_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (user_id, network_id) DO UPDATE SET role = $3, policy_id = $4
	`
	_, err := r.db.ExecContext(ctx, query, userID, networkID, role, policyID)
	if err != nil {
		return fmt.Errorf("分配用户到网络失败：%w", err)
	}
	return nil
}

// RemoveUser 从网络移除用户
func (r *NetworkRepository) RemoveUser(ctx context.Context, networkID, userID uuid.UUID) error {
	query := `DELETE FROM user_networks WHERE user_id = $1 AND network_id = $2`
	_, err := r.db.ExecContext(ctx, query, userID, networkID)
	if err != nil {
		return fmt.Errorf("从网络移除用户失败：%w", err)
	}
	return nil
}

// ListUsers 获取网络下的用户列表
func (r *NetworkRepository) ListUsers(ctx context.Context, networkID uuid.UUID, params *model.PageParams) ([]model.User, int64, error) {
	countQuery := `SELECT COUNT(*) FROM user_networks WHERE network_id = $1`
	listQuery := `
		SELECT u.id, u.username, u.email, u.phone, u.password_hash, u.role, u.enabled, u.created_at, u.updated_at
		FROM users u
		INNER JOIN user_networks un ON u.id = un.user_id
		WHERE un.network_id = $1
	`

	var total int64
	err := r.db.QueryRowContext(ctx, countQuery, networkID).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("统计网络用户数量失败：%w", err)
	}

	listQuery += fmt.Sprintf(" ORDER BY un.created_at DESC LIMIT $2 OFFSET $3")
	rows, err := r.db.QueryContext(ctx, listQuery, networkID, params.Limit, params.GetOffset())
	if err != nil {
		return nil, 0, fmt.Errorf("查询网络用户列表失败：%w", err)
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		var user model.User
		err := rows.Scan(
			&user.ID, &user.Username, &user.Email, &user.Phone, &user.PasswordHash,
			&user.Role, &user.Enabled, &user.CreatedAt, &user.UpdatedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("扫描用户行失败：%w", err)
		}
		users = append(users, user)
	}

	return users, total, nil
}

// ListPeers 获取网络下的节点列表
func (r *NetworkRepository) ListPeers(ctx context.Context, networkID uuid.UUID) ([]model.Peer, error) {
	query := `
		SELECT id, device_id, network_id, public_ip, public_port, nat_type, online, assigned_ip,
		       hostname, tunnel_type, created_at, updated_at, last_seen_at
		FROM peers WHERE network_id = $1
	`
	rows, err := r.db.QueryContext(ctx, query, networkID)
	if err != nil {
		return nil, fmt.Errorf("查询网络节点列表失败：%w", err)
	}
	defer rows.Close()

	var peers []model.Peer
	for rows.Next() {
		var peer model.Peer
		err := rows.Scan(
			&peer.ID, &peer.DeviceID, &peer.NetworkID, &peer.PublicIP, &peer.PublicPort,
			&peer.NATType, &peer.Online, &peer.AssignedIP, &peer.Hostname, &peer.TunnelType,
			&peer.CreatedAt, &peer.UpdatedAt, &peer.LastSeenAt,
		)
		if err != nil {
			return nil, fmt.Errorf("扫描节点行失败：%w", err)
		}
		peers = append(peers, peer)
	}

	return peers, nil
}
