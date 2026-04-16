package model

import (
	"time"

	"github.com/google/uuid"
)

type Device struct {
	ID          uuid.UUID  `json:"id"`
	DeviceID    string     `json:"device_id"`
	UserID      *uuid.UUID `json:"user_id,omitempty"`
	Hostname    *string    `json:"hostname,omitempty"`
	PublicIP    *string    `json:"public_ip,omitempty"`
	NATType     string     `json:"nat_type"`
	AssignedIP  *string    `json:"assigned_ip,omitempty"`
	Online      bool       `json:"online"`
	ClientVersion *string  `json:"client_version,omitempty"`
	OSType      *string    `json:"os_type,omitempty"`
	OSVersion   *string    `json:"os_version,omitempty"`
	FirstSeenAt time.Time  `json:"first_seen_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	Enabled     bool       `json:"enabled"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type DeviceBindRequest struct {
	DeviceID string `json:"device_id" binding:"required"`
	UserID   string `json:"user_id" binding:"required"`
}

type DeviceUnbindRequest struct {
	DeviceID string `json:"device_id" binding:"required"`
}

type DeviceTransferRequest struct {
	DeviceID string `json:"device_id" binding:"required"`
	FromUser string `json:"from_user" binding:"required"`
	ToUser   string `json:"to_user" binding:"required"`
}
