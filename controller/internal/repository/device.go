package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
)

type DeviceRepository struct {
	db *PostgresDB
}

func NewDeviceRepository(db *PostgresDB) *DeviceRepository {
	return &DeviceRepository{db: db}
}

func (r *DeviceRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.Device, error) {
	query := `
		SELECT id, device_id, user_id, hostname, public_ip, nat_type, assigned_ip, online,
		       client_version, os_type, os_version, first_seen_at, last_seen_at, enabled, created_at, updated_at
		FROM devices WHERE id = $1
	`
	device := &model.Device{}
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&device.ID, &device.DeviceID, &device.UserID, &device.Hostname, &device.PublicIP,
		&device.NATType, &device.AssignedIP, &device.Online, &device.ClientVersion,
		&device.OSType, &device.OSVersion, &device.FirstSeenAt, &device.LastSeenAt,
		&device.Enabled, &device.CreatedAt, &device.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询设备失败：%w", err)
	}
	return device, nil
}

func (r *DeviceRepository) GetByDeviceID(ctx context.Context, deviceID string) (*model.Device, error) {
	query := `
		SELECT id, device_id, user_id, hostname, public_ip, nat_type, assigned_ip, online,
		       client_version, os_type, os_version, first_seen_at, last_seen_at, enabled, created_at, updated_at
		FROM devices WHERE device_id = $1
	`
	device := &model.Device{}
	err := r.db.QueryRowContext(ctx, query, deviceID).Scan(
		&device.ID, &device.DeviceID, &device.UserID, &device.Hostname, &device.PublicIP,
		&device.NATType, &device.AssignedIP, &device.Online, &device.ClientVersion,
		&device.OSType, &device.OSVersion, &device.FirstSeenAt, &device.LastSeenAt,
		&device.Enabled, &device.CreatedAt, &device.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询设备失败：%w", err)
	}
	return device, nil
}

func (r *DeviceRepository) Create(ctx context.Context, device *model.Device) error {
	query := `
		INSERT INTO devices (id, device_id, user_id, hostname, public_ip, nat_type, assigned_ip, online,
		                     client_version, os_type, os_version, first_seen_at, last_seen_at, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`
	_, err := r.db.ExecContext(ctx, query,
		device.ID, device.DeviceID, device.UserID, device.Hostname, device.PublicIP,
		device.NATType, device.AssignedIP, device.Online, device.ClientVersion,
		device.OSType, device.OSVersion, device.FirstSeenAt, device.LastSeenAt,
		device.Enabled, device.CreatedAt, device.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("创建设备失败：%w", err)
	}
	return nil
}

func (r *DeviceRepository) List(ctx context.Context, params *model.PageParams) ([]model.Device, int64, error) {
	countQuery := `SELECT COUNT(*) FROM devices WHERE 1=1`
	listQuery := `
		SELECT id, device_id, user_id, hostname, public_ip, nat_type, assigned_ip, online,
		       client_version, os_type, os_version, first_seen_at, last_seen_at, enabled, created_at, updated_at
		FROM devices WHERE 1=1
	`

	args := []interface{}{}
	argIdx := 1

	if params.Search != "" {
		searchPattern := "%" + params.Search + "%"
		countQuery += fmt.Sprintf(" AND (device_id LIKE $%d OR hostname LIKE $%d)", argIdx, argIdx)
		listQuery += fmt.Sprintf(" AND (device_id LIKE $%d OR hostname LIKE $%d)", argIdx, argIdx)
		args = append(args, searchPattern)
		argIdx++
	}

	var total int64
	err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("统计设备数量失败：%w", err)
	}

	listQuery += fmt.Sprintf(" ORDER BY last_seen_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, params.Limit, params.GetOffset())

	rows, err := r.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询设备列表失败：%w", err)
	}
	defer rows.Close()

	var devices []model.Device
	for rows.Next() {
		var device model.Device
		err := rows.Scan(
			&device.ID, &device.DeviceID, &device.UserID, &device.Hostname, &device.PublicIP,
			&device.NATType, &device.AssignedIP, &device.Online, &device.ClientVersion,
			&device.OSType, &device.OSVersion, &device.FirstSeenAt, &device.LastSeenAt,
			&device.Enabled, &device.CreatedAt, &device.UpdatedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("扫描设备行失败：%w", err)
		}
		devices = append(devices, device)
	}

	return devices, total, nil
}

func (r *DeviceRepository) Update(ctx context.Context, id uuid.UUID, req *model.Device) error {
	query := `
		UPDATE devices SET hostname = $1, public_ip = $2, nat_type = $3, assigned_ip = $4,
		                  online = $5, client_version = $6, os_type = $7, os_version = $8,
		                  last_seen_at = $9, enabled = $10, updated_at = NOW()
		WHERE id = $11
	`
	_, err := r.db.ExecContext(ctx, query,
		req.Hostname, req.PublicIP, req.NATType, req.AssignedIP, req.Online,
		req.ClientVersion, req.OSType, req.OSVersion, req.LastSeenAt, req.Enabled, id,
	)
	if err != nil {
		return fmt.Errorf("更新设备失败：%w", err)
	}
	return nil
}

func (r *DeviceRepository) Bind(ctx context.Context, deviceID string, userID uuid.UUID) error {
	query := `UPDATE devices SET user_id = $1, updated_at = NOW() WHERE device_id = $2`
	_, err := r.db.ExecContext(ctx, query, userID, deviceID)
	if err != nil {
		return fmt.Errorf("绑定设备失败：%w", err)
	}
	return nil
}

func (r *DeviceRepository) Unbind(ctx context.Context, deviceID string) error {
	query := `UPDATE devices SET user_id = NULL, updated_at = NOW() WHERE device_id = $1`
	_, err := r.db.ExecContext(ctx, query, deviceID)
	if err != nil {
		return fmt.Errorf("解绑设备失败：%w", err)
	}
	return nil
}

func (r *DeviceRepository) UpdateLastSeen(ctx context.Context, deviceID string) error {
	query := `UPDATE devices SET last_seen_at = $1, online = TRUE, updated_at = NOW() WHERE device_id = $2`
	_, err := r.db.ExecContext(ctx, query, time.Now(), deviceID)
	if err != nil {
		return fmt.Errorf("更新设备最后在线时间失败：%w", err)
	}
	return nil
}

func (r *DeviceRepository) SetOffline(ctx context.Context, deviceID string) error {
	query := `UPDATE devices SET online = FALSE, updated_at = NOW() WHERE device_id = $1`
	_, err := r.db.ExecContext(ctx, query, deviceID)
	if err != nil {
		return fmt.Errorf("设置设备离线失败：%w", err)
	}
	return nil
}
