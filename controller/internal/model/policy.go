package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Policy struct {
	ID         uuid.UUID       `json:"id"`
	NetworkID  uuid.UUID       `json:"network_id"`
	Name       string          `json:"name"`
	PolicyType string          `json:"policy_type"`
	Config     json.RawMessage `json:"config"`
	Priority   int             `json:"priority"`
	Enabled    bool            `json:"enabled"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	CreatedBy  *uuid.UUID      `json:"created_by,omitempty"`
}

type PolicyCreateRequest struct {
	NetworkID  uuid.UUID       `json:"network_id" binding:"required"`
	Name       string          `json:"name" binding:"required,min=2,max=128"`
	PolicyType string          `json:"policy_type" binding:"required,oneof=route dns url_filter traffic_limit time_range audit"`
	Config     json.RawMessage `json:"config" binding:"required"`
	Priority   int             `json:"priority"`
	Enabled    *bool           `json:"enabled"`
}

type PolicyUpdateRequest struct {
	Name     string          `json:"name" binding:"omitempty,min=2,max=128"`
	Config   json.RawMessage `json:"config"`
	Priority *int            `json:"priority"`
	Enabled  *bool           `json:"enabled"`
}

type PolicyStatusRequest struct {
	Enabled bool `json:"enabled"`
}
