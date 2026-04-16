package model

import (
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID           uuid.UUID  `json:"id"`
	Username     string     `json:"username"`
	Email        string     `json:"email"`
	Phone        *string    `json:"phone,omitempty"`
	PasswordHash string     `json:"-"`
	Role         string     `json:"role"`
	Enabled      bool       `json:"enabled"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type UserCreateRequest struct {
	Username string `json:"username" binding:"required,min=3,max=64"`
	Email    string `json:"email" binding:"required,email"`
	Phone    string `json:"phone"`
	Password string `json:"password" binding:"required,min=8"`
	Role     string `json:"role" binding:"required,oneof=super_admin network_admin user"`
}

type UserUpdateRequest struct {
	Email   string `json:"email" binding:"omitempty,email"`
	Phone   string `json:"phone"`
	Role    string `json:"role" binding:"omitempty,oneof=super_admin network_admin user"`
	Enabled *bool  `json:"enabled"`
}

type UserListResponse struct {
	Users      []User `json:"users"`
	Total      int64  `json:"total"`
	Page       int    `json:"page"`
	Limit      int    `json:"limit"`
}

type ChangePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password" binding:"required,min=8"`
}
