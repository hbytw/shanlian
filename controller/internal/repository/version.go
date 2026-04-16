package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
)

// VersionRepository 版本仓储
type VersionRepository struct {
	db *sql.DB
}

// NewVersionRepository 创建版本仓储
func NewVersionRepository(db *sql.DB) *VersionRepository {
	return &VersionRepository{db: db}
}

// List 获取版本列表
func (r *VersionRepository) List(ctx context.Context, platform string, enabledOnly bool) ([]*model.Version, error) {
	query := `
		SELECT id, version, min_supported, download_url, sha256, file_size, platform, force_update, enabled, created_at, created_by
		FROM versions
		WHERE 1=1
	`
	args := []interface{}{}
	argIdx := 1

	if platform != "" {
		query += fmt.Sprintf(" AND platform = $%d", argIdx)
		args = append(args, platform)
		argIdx++
	}
	if enabledOnly {
		query += " AND enabled = true"
	}
	query += " ORDER BY created_at DESC"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询版本列表失败：%w", err)
	}
	defer rows.Close()

	var versions []*model.Version
	for rows.Next() {
		v := &model.Version{}
		err := rows.Scan(
			&v.ID, &v.Version, &v.MinSupported, &v.DownloadURL, &v.SHA256,
			&v.FileSize, &v.Platform, &v.ForceUpdate, &v.Enabled, &v.CreatedAt, &v.CreatedBy,
		)
		if err != nil {
			return nil, fmt.Errorf("扫描版本失败：%w", err)
		}
		versions = append(versions, v)
	}
	return versions, nil
}

// GetLatest 获取最新版本
func (r *VersionRepository) GetLatest(ctx context.Context, platform string) (*model.Version, error) {
	query := `
		SELECT id, version, min_supported, download_url, sha256, file_size, platform, force_update, enabled, created_at, created_by
		FROM versions
		WHERE platform = $1 AND enabled = true
		ORDER BY created_at DESC
		LIMIT 1
	`
	v := &model.Version{}
	err := r.db.QueryRowContext(ctx, query, platform).Scan(
		&v.ID, &v.Version, &v.MinSupported, &v.DownloadURL, &v.SHA256,
		&v.FileSize, &v.Platform, &v.ForceUpdate, &v.Enabled, &v.CreatedAt, &v.CreatedBy,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询最新版本失败：%w", err)
	}
	return v, nil
}

// GetByID 根据 ID 获取版本
func (r *VersionRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.Version, error) {
	query := `
		SELECT id, version, min_supported, download_url, sha256, file_size, platform, force_update, enabled, created_at, created_by
		FROM versions
		WHERE id = $1
	`
	v := &model.Version{}
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&v.ID, &v.Version, &v.MinSupported, &v.DownloadURL, &v.SHA256,
		&v.FileSize, &v.Platform, &v.ForceUpdate, &v.Enabled, &v.CreatedAt, &v.CreatedBy,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询版本失败：%w", err)
	}
	return v, nil
}

// Create 创建版本
func (r *VersionRepository) Create(ctx context.Context, v *model.Version) error {
	query := `
		INSERT INTO versions (id, version, min_supported, download_url, sha256, file_size, platform, force_update, enabled, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err := r.db.ExecContext(ctx, query,
		v.ID, v.Version, v.MinSupported, v.DownloadURL, v.SHA256,
		v.FileSize, v.Platform, v.ForceUpdate, v.Enabled, v.CreatedBy,
	)
	if err != nil {
		return fmt.Errorf("创建版本失败：%w", err)
	}
	return nil
}

// Update 更新版本
func (r *VersionRepository) Update(ctx context.Context, v *model.Version) error {
	query := `
		UPDATE versions
		SET min_supported = $2, download_url = $3, sha256 = $4, file_size = $5,
		    force_update = $6, enabled = $7
		WHERE id = $1
	`
	_, err := r.db.ExecContext(ctx, query,
		v.ID, v.MinSupported, v.DownloadURL, v.SHA256, v.FileSize, v.ForceUpdate, v.Enabled,
	)
	if err != nil {
		return fmt.Errorf("更新版本失败：%w", err)
	}
	return nil
}

// Delete 删除版本
func (r *VersionRepository) Delete(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM versions WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("删除版本失败：%w", err)
	}
	return nil
}
