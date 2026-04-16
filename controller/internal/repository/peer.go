package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
)

type PeerRepository struct {
	db *PostgresDB
}

func NewPeerRepository(db *PostgresDB) *PeerRepository {
	return &PeerRepository{db: db}
}

func (r *PeerRepository) Create(ctx context.Context, peer *model.Peer) error {
	query := `
		INSERT INTO peers (id, device_id, network_id, public_ip, public_port, nat_type, online,
		                   assigned_ip, hostname, tunnel_type, created_at, updated_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`
	_, err := r.db.ExecContext(ctx, query,
		peer.ID, peer.DeviceID, peer.NetworkID, peer.PublicIP, peer.PublicPort,
		peer.NATType, peer.Online, peer.AssignedIP, peer.Hostname, peer.TunnelType,
		peer.CreatedAt, peer.UpdatedAt, peer.LastSeenAt,
	)
	if err != nil {
		return fmt.Errorf("创建节点失败：%w", err)
	}
	return nil
}

func (r *PeerRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.Peer, error) {
	query := `
		SELECT id, device_id, network_id, public_ip, public_port, nat_type, online,
		       assigned_ip, hostname, tunnel_type, created_at, updated_at, last_seen_at
		FROM peers WHERE id = $1
	`
	peer := &model.Peer{}
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&peer.ID, &peer.DeviceID, &peer.NetworkID, &peer.PublicIP, &peer.PublicPort,
		&peer.NATType, &peer.Online, &peer.AssignedIP, &peer.Hostname, &peer.TunnelType,
		&peer.CreatedAt, &peer.UpdatedAt, &peer.LastSeenAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询节点失败：%w", err)
	}
	return peer, nil
}

func (r *PeerRepository) GetByDeviceID(ctx context.Context, deviceID uuid.UUID) (*model.Peer, error) {
	query := `SELECT id, device_id, network_id, public_ip, public_port, nat_type, online,
	         assigned_ip, hostname, tunnel_type, created_at, updated_at, last_seen_at
	         FROM peers WHERE device_id = $1`
	peer := &model.Peer{}
	err := r.db.QueryRowContext(ctx, query, deviceID).Scan(
		&peer.ID, &peer.DeviceID, &peer.NetworkID, &peer.PublicIP, &peer.PublicPort,
		&peer.NATType, &peer.Online, &peer.AssignedIP, &peer.Hostname, &peer.TunnelType,
		&peer.CreatedAt, &peer.UpdatedAt, &peer.LastSeenAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询节点失败：%w", err)
	}
	return peer, nil
}

func (r *PeerRepository) List(ctx context.Context, params *model.PageParams) ([]model.Peer, int64, error) {
	countQuery := `SELECT COUNT(*) FROM peers WHERE 1=1`
	listQuery := `SELECT id, device_id, network_id, public_ip, public_port, nat_type, online,
	              assigned_ip, hostname, tunnel_type, created_at, updated_at, last_seen_at
	              FROM peers WHERE 1=1`

	args := []interface{}{}
	argIdx := 1

	if params.Search != "" {
		searchPattern := "%" + params.Search + "%"
		countQuery += fmt.Sprintf(" AND hostname LIKE $%d", argIdx)
		listQuery += fmt.Sprintf(" AND hostname LIKE $%d", argIdx)
		args = append(args, searchPattern)
		argIdx++
	}

	var total int64
	err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("统计节点数量失败：%w", err)
	}

	listQuery += fmt.Sprintf(" ORDER BY last_seen_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, params.Limit, params.GetOffset())

	rows, err := r.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询节点列表失败：%w", err)
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
			return nil, 0, fmt.Errorf("扫描节点行失败：%w", err)
		}
		peers = append(peers, peer)
	}

	return peers, total, nil
}

func (r *PeerRepository) Update(ctx context.Context, id uuid.UUID, req *model.Peer) error {
	query := `UPDATE peers SET public_ip = $1, public_port = $2, nat_type = $3, online = $4,
	          hostname = $5, last_seen_at = $6, updated_at = NOW() WHERE id = $7`
	_, err := r.db.ExecContext(ctx, query,
		req.PublicIP, req.PublicPort, req.NATType, req.Online, req.Hostname, req.LastSeenAt, id,
	)
	if err != nil {
		return fmt.Errorf("更新节点失败：%w", err)
	}
	return nil
}

func (r *PeerRepository) SetOnline(ctx context.Context, id uuid.UUID) error {
	now := time.Now()
	query := `UPDATE peers SET online = TRUE, last_seen_at = $1, updated_at = NOW() WHERE id = $2`
	_, err := r.db.ExecContext(ctx, query, now, id)
	if err != nil {
		return fmt.Errorf("设置节点在线失败：%w", err)
	}
	return nil
}

func (r *PeerRepository) SetOffline(ctx context.Context, id uuid.UUID) error {
	query := `UPDATE peers SET online = FALSE, updated_at = NOW() WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("设置节点离线失败：%w", err)
	}
	return nil
}

func (r *PeerRepository) Delete(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM peers WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("删除节点失败：%w", err)
	}
	return nil
}

func (r *PeerRepository) DeleteByNetwork(ctx context.Context, networkID, peerID uuid.UUID) error {
	query := `DELETE FROM peers WHERE id = $1 AND network_id = $2`
	_, err := r.db.ExecContext(ctx, query, peerID, networkID)
	if err != nil {
		return fmt.Errorf("删除网络节点失败：%w", err)
	}
	return nil
}

// GetOnlinePeers 获取在线节点
func (r *PeerRepository) GetOnlinePeers(ctx context.Context) ([]model.Peer, error) {
	query := `SELECT id, device_id, network_id, public_ip, public_port, nat_type, online,
	          assigned_ip, hostname, tunnel_type, created_at, updated_at, last_seen_at
	          FROM peers WHERE online = TRUE`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("查询在线节点失败：%w", err)
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

// GetNetworkPeers 获取指定网络的节点
func (r *PeerRepository) GetNetworkPeers(ctx context.Context, networkID uuid.UUID) ([]model.Peer, error) {
	query := `SELECT id, device_id, network_id, public_ip, public_port, nat_type, online,
	          assigned_ip, hostname, tunnel_type, created_at, updated_at, last_seen_at
	          FROM peers WHERE network_id = $1`
	rows, err := r.db.QueryContext(ctx, query, networkID)
	if err != nil {
		return nil, fmt.Errorf("查询网络节点失败：%w", err)
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
