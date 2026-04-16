package model

import (
	"time"

	"github.com/google/uuid"
)

type Network struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	CIDR        string     `json:"cidr"`
	GatewayIP   string     `json:"gateway_ip"`
	Description *string    `json:"description,omitempty"`
	Enabled     bool       `json:"enabled"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CreatedBy   *uuid.UUID `json:"created_by,omitempty"`
}

type NetworkCreateRequest struct {
	Name        string  `json:"name" binding:"required,min=2,max=128"`
	CIDR        string  `json:"cidr" binding:"required"`
	GatewayIP   string  `json:"gateway_ip" binding:"required"`
	Description string  `json:"description"`
	Enabled     *bool   `json:"enabled"`
}

type NetworkUpdateRequest struct {
	Name        string  `json:"name" binding:"omitempty,min=2,max=128"`
	Description string  `json:"description"`
	Enabled     *bool   `json:"enabled"`
}

type UserNetwork struct {
	UserID    uuid.UUID `json:"user_id"`
	NetworkID uuid.UUID `json:"network_id"`
	Role      string    `json:"role"`
	PolicyID  *uuid.UUID `json:"policy_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type AssignUserRequest struct {
	UserID   uuid.UUID `json:"user_id" binding:"required"`
	Role     string    `json:"role" binding:"required,oneof=owner admin member"`
	PolicyID uuid.UUID `json:"policy_id"`
}

type UpdateUserPolicyRequest struct {
	Role     string    `json:"role" binding:"required,oneof=owner admin member"`
	PolicyID uuid.UUID `json:"policy_id"`
}
