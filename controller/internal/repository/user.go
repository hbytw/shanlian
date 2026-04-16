package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
)

// UserRepository 用户仓储
type UserRepository struct {
	db *PostgresDB
}

// NewUserRepository 创建用户仓储
func NewUserRepository(db *PostgresDB) *UserRepository {
	return &UserRepository{db: db}
}

// Create 创建用户
func (r *UserRepository) Create(ctx context.Context, req *model.UserCreateRequest, passwordHash string) (*model.User, error) {
	user := &model.User{
		ID:           uuid.New(),
		Username:     req.Username,
		Email:        req.Email,
		PasswordHash: passwordHash,
		Role:         req.Role,
		Enabled:      true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if req.Phone != "" {
		user.Phone = &req.Phone
	}

	query := `
		INSERT INTO users (id, username, email, phone, password_hash, role, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := r.db.ExecContext(ctx, query,
		user.ID, user.Username, user.Email, user.Phone, user.PasswordHash,
		user.Role, user.Enabled, user.CreatedAt, user.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("创建用户失败：%w", err)
	}

	return user, nil
}

// GetByID 根据 ID 获取用户
func (r *UserRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.User, error) {
	query := `
		SELECT id, username, email, phone, password_hash, role, enabled, created_at, updated_at
		FROM users WHERE id = $1
	`
	user := &model.User{}
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&user.ID, &user.Username, &user.Email, &user.Phone, &user.PasswordHash,
		&user.Role, &user.Enabled, &user.CreatedAt, &user.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询用户失败：%w", err)
	}
	return user, nil
}

// GetByUsername 根据用户名获取用户
func (r *UserRepository) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	query := `
		SELECT id, username, email, phone, password_hash, role, enabled, created_at, updated_at
		FROM users WHERE username = $1
	`
	user := &model.User{}
	err := r.db.QueryRowContext(ctx, query, username).Scan(
		&user.ID, &user.Username, &user.Email, &user.Phone, &user.PasswordHash,
		&user.Role, &user.Enabled, &user.CreatedAt, &user.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询用户失败：%w", err)
	}
	return user, nil
}

// GetByEmail 根据邮箱获取用户
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	query := `
		SELECT id, username, email, phone, password_hash, role, enabled, created_at, updated_at
		FROM users WHERE email = $1
	`
	user := &model.User{}
	err := r.db.QueryRowContext(ctx, query, email).Scan(
		&user.ID, &user.Username, &user.Email, &user.Phone, &user.PasswordHash,
		&user.Role, &user.Enabled, &user.CreatedAt, &user.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询用户失败：%w", err)
	}
	return user, nil
}

// GetByPhone 根据手机号获取用户
func (r *UserRepository) GetByPhone(ctx context.Context, phone string) (*model.User, error) {
	query := `
		SELECT id, username, email, phone, password_hash, role, enabled, created_at, updated_at
		FROM users WHERE phone = $1
	`
	user := &model.User{}
	err := r.db.QueryRowContext(ctx, query, phone).Scan(
		&user.ID, &user.Username, &user.Email, &user.Phone, &user.PasswordHash,
		&user.Role, &user.Enabled, &user.CreatedAt, &user.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询用户失败：%w", err)
	}
	return user, nil
}

// List 分页获取用户列表
func (r *UserRepository) List(ctx context.Context, params *model.PageParams) ([]model.User, int64, error) {
	countQuery := `SELECT COUNT(*) FROM users WHERE 1=1`
	listQuery := `
		SELECT id, username, email, phone, password_hash, role, enabled, created_at, updated_at
		FROM users WHERE 1=1
	`

	// 动态构建查询
	args := []interface{}{}
	argIdx := 1

	if params.Search != "" {
		searchPattern := "%" + params.Search + "%"
		countQuery += fmt.Sprintf(" AND (username LIKE $%d OR email LIKE $%d)", argIdx, argIdx)
		listQuery += fmt.Sprintf(" AND (username LIKE $%d OR email LIKE $%d)", argIdx, argIdx)
		args = append(args, searchPattern)
		argIdx++
	}

	// 获取总数
	var total int64
	err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("统计用户数量失败：%w", err)
	}

	// 添加分页
	listQuery += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, params.Limit, params.GetOffset())

	rows, err := r.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询用户列表失败：%w", err)
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

// Update 更新用户
func (r *UserRepository) Update(ctx context.Context, id uuid.UUID, req *model.UserUpdateRequest) error {
	query := `UPDATE users SET email = $1, phone = $2, role = $3, enabled = $4, updated_at = NOW() WHERE id = $5`
	_, err := r.db.ExecContext(ctx, query, req.Email, req.Phone, req.Role, req.Enabled, id)
	if err != nil {
		return fmt.Errorf("更新用户失败：%w", err)
	}
	return nil
}

// Delete 删除用户
func (r *UserRepository) Delete(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM users WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("删除用户失败：%w", err)
	}
	return nil
}

// UpdatePassword 更新密码
func (r *UserRepository) UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string) error {
	query := `UPDATE users SET password_hash = $1, updated_at = NOW() WHERE id = $2`
	_, err := r.db.ExecContext(ctx, query, passwordHash, id)
	if err != nil {
		return fmt.Errorf("更新密码失败：%w", err)
	}
	return nil
}

// UpdateLastLogin 更新最后登录时间
func (r *UserRepository) UpdateLastLogin(ctx context.Context, id uuid.UUID) error {
	query := `UPDATE users SET updated_at = NOW() WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("更新最后登录时间失败：%w", err)
	}
	return nil
}
