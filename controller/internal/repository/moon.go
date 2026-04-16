package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
)

type MoonRepository struct {
	db *PostgresDB
}

func NewMoonRepository(db *PostgresDB) *MoonRepository {
	return &MoonRepository{db: db}
}

func (r *MoonRepository) generateAPIKey() (string, string, error) {
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return "", "", err
	}
	apiKey := hex.EncodeToString(keyBytes[:16])
	apiSecret := hex.EncodeToString(keyBytes[16:])
	return apiKey, apiSecret, nil
}

func (r *MoonRepository) Create(ctx context.Context, req *model.MoonCreateRequest) (*model.MoonNode, error) {
	apiKey, apiSecret, err := r.generateAPIKey()
	if err != nil {
		return nil, fmt.Errorf("生成 API 密钥失败：%w", err)
	}

	moon := &model.MoonNode{
		ID:         uuid.New(),
		Domain:     req.Domain,
		PublicIP:   req.PublicIP,
		GatewayIP:  req.GatewayIP,
		RelayPort:  11002,
		WSSPort:    11443,
		UDPPort:    11001,
		HealthPort: 11003,
		Weight:     100,
		APIKey:     apiKey,
		APISecret:  apiSecret,
		Online:     false,
		Connections: 0,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if req.RelayPort > 0 {
		moon.RelayPort = req.RelayPort
	}
	if req.WSSPort > 0 {
		moon.WSSPort = req.WSSPort
	}
	if req.UDPPort > 0 {
		moon.UDPPort = req.UDPPort
	}
	if req.HealthPort > 0 {
		moon.HealthPort = req.HealthPort
	}
	if req.Weight > 0 {
		moon.Weight = req.Weight
	}

	query := `
		INSERT INTO moon_nodes (id, domain, public_ip, gateway_ip, relay_port, wss_port, udp_port,
		                        health_port, weight, api_key, api_secret, online, connections, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`
	_, err = r.db.ExecContext(ctx, query,
		moon.ID, moon.Domain, moon.PublicIP, moon.GatewayIP, moon.RelayPort, moon.WSSPort,
		moon.UDPPort, moon.HealthPort, moon.Weight, moon.APIKey, moon.APISecret,
		moon.Online, moon.Connections, moon.CreatedAt, moon.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("创建 Moon 节点失败：%w", err)
	}
	return moon, nil
}

func (r *MoonRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.MoonNode, error) {
	query := `
		SELECT id, domain, public_ip, gateway_ip, relay_port, wss_port, udp_port, health_port,
		       weight, api_key, api_secret, online, version, connections, created_at, updated_at, last_heartbeat_at
		FROM moon_nodes WHERE id = $1
	`
	moon := &model.MoonNode{}
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&moon.ID, &moon.Domain, &moon.PublicIP, &moon.GatewayIP, &moon.RelayPort, &moon.WSSPort,
		&moon.UDPPort, &moon.HealthPort, &moon.Weight, &moon.APIKey, &moon.APISecret,
		&moon.Online, &moon.Version, &moon.Connections, &moon.CreatedAt, &moon.UpdatedAt, &moon.LastHeartbeatAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询 Moon 节点失败：%w", err)
	}
	return moon, nil
}

func (r *MoonRepository) GetByDomain(ctx context.Context, domain string) (*model.MoonNode, error) {
	query := `SELECT id, domain, public_ip, gateway_ip, relay_port, wss_port, udp_port, health_port,
	          weight, api_key, api_secret, online, version, connections, created_at, updated_at, last_heartbeat_at
	          FROM moon_nodes WHERE domain = $1`
	moon := &model.MoonNode{}
	err := r.db.QueryRowContext(ctx, query, domain).Scan(
		&moon.ID, &moon.Domain, &moon.PublicIP, &moon.GatewayIP, &moon.RelayPort, &moon.WSSPort,
		&moon.UDPPort, &moon.HealthPort, &moon.Weight, &moon.APIKey, &moon.APISecret,
		&moon.Online, &moon.Version, &moon.Connections, &moon.CreatedAt, &moon.UpdatedAt, &moon.LastHeartbeatAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询 Moon 节点失败：%w", err)
	}
	return moon, nil
}

func (r *MoonRepository) GetByAPIKey(ctx context.Context, apiKey string) (*model.MoonNode, error) {
	query := `SELECT id, domain, public_ip, gateway_ip, relay_port, wss_port, udp_port, health_port,
	          weight, api_key, api_secret, online, version, connections, created_at, updated_at, last_heartbeat_at
	          FROM moon_nodes WHERE api_key = $1`
	moon := &model.MoonNode{}
	err := r.db.QueryRowContext(ctx, query, apiKey).Scan(
		&moon.ID, &moon.Domain, &moon.PublicIP, &moon.GatewayIP, &moon.RelayPort, &moon.WSSPort,
		&moon.UDPPort, &moon.HealthPort, &moon.Weight, &moon.APIKey, &moon.APISecret,
		&moon.Online, &moon.Version, &moon.Connections, &moon.CreatedAt, &moon.UpdatedAt, &moon.LastHeartbeatAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询 Moon 节点失败：%w", err)
	}
	return moon, nil
}

func (r *MoonRepository) List(ctx context.Context, params *model.PageParams) ([]model.MoonNode, int64, error) {
	countQuery := `SELECT COUNT(*) FROM moon_nodes WHERE 1=1`
	listQuery := `SELECT id, domain, public_ip, gateway_ip, relay_port, wss_port, udp_port, health_port,
	              weight, api_key, api_secret, online, version, connections, created_at, updated_at, last_heartbeat_at
	              FROM moon_nodes WHERE 1=1`

	args := []interface{}{}
	argIdx := 1

	if params.Search != "" {
		searchPattern := "%" + params.Search + "%"
		countQuery += fmt.Sprintf(" AND domain LIKE $%d", argIdx)
		listQuery += fmt.Sprintf(" AND domain LIKE $%d", argIdx)
		args = append(args, searchPattern)
		argIdx++
	}

	var total int64
	err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("统计 Moon 节点数量失败：%w", err)
	}

	listQuery += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, params.Limit, params.GetOffset())

	rows, err := r.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询 Moon 节点列表失败：%w", err)
	}
	defer rows.Close()

	var moons []model.MoonNode
	for rows.Next() {
		var moon model.MoonNode
		err := rows.Scan(
			&moon.ID, &moon.Domain, &moon.PublicIP, &moon.GatewayIP, &moon.RelayPort, &moon.WSSPort,
			&moon.UDPPort, &moon.HealthPort, &moon.Weight, &moon.APIKey, &moon.APISecret,
			&moon.Online, &moon.Version, &moon.Connections, &moon.CreatedAt, &moon.UpdatedAt, &moon.LastHeartbeatAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("扫描 Moon 节点行失败：%w", err)
		}
		moons = append(moons, moon)
	}

	return moons, total, nil
}

func (r *MoonRepository) Update(ctx context.Context, id uuid.UUID, req *model.MoonUpdateRequest) error {
	query := `UPDATE moon_nodes SET public_ip = $1, gateway_ip = $2, weight = $3, updated_at = NOW() WHERE id = $4`
	_, err := r.db.ExecContext(ctx, query, req.PublicIP, req.GatewayIP, req.Weight, id)
	if err != nil {
		return fmt.Errorf("更新 Moon 节点失败：%w", err)
	}
	return nil
}

func (r *MoonRepository) Delete(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM moon_nodes WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("删除 Moon 节点失败：%w", err)
	}
	return nil
}

func (r *MoonRepository) UpdateHeartbeat(ctx context.Context, domain string, version string) error {
	query := `UPDATE moon_nodes SET online = TRUE, version = $1, last_heartbeat_at = NOW(), updated_at = NOW() WHERE domain = $2`
	_, err := r.db.ExecContext(ctx, query, version, domain)
	if err != nil {
		return fmt.Errorf("更新 Moon 心跳失败：%w", err)
	}
	return nil
}

func (r *MoonRepository) SetOffline(ctx context.Context, domain string) error {
	query := `UPDATE moon_nodes SET online = FALSE, updated_at = NOW() WHERE domain = $1`
	_, err := r.db.ExecContext(ctx, query, domain)
	if err != nil {
		return fmt.Errorf("设置 Moon 离线失败：%w", err)
	}
	return nil
}

// GetNetworkByID 获取网络
func (r *MoonRepository) GetNetworkByID(ctx context.Context, networkID uuid.UUID) (*model.Network, error) {
	query := `SELECT id, name, cidr, gateway_ip, description, enabled, created_at, updated_at, created_by FROM networks WHERE id = $1`
	network := &model.Network{}
	err := r.db.QueryRowContext(ctx, query, networkID).Scan(
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

// AllocateMoonIP 为 Moon 分配网络 IP
func (r *MoonRepository) AllocateMoonIP(ctx context.Context, networkID, moonID uuid.UUID) (string, error) {
	// 获取网络信息
	network, err := r.GetNetworkByID(ctx, networkID)
	if err != nil {
		return "", err
	}
	if network == nil {
		return "", fmt.Errorf("网络不存在")
	}

	// 简单实现：从网关 IP 开始分配
	// 实际生产环境需要更复杂的 IPAM
	assignedIP := network.GatewayIP
	return assignedIP, nil
}

// ReleaseMoonIP 释放 Moon 的网络 IP
func (r *MoonRepository) ReleaseMoonIP(ctx context.Context, moonID uuid.UUID) error {
	// 实现 IP 释放逻辑
	return nil
}
