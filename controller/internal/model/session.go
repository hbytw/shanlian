package model

import (
	"time"

	"github.com/google/uuid"
)

type Session struct {
	ID             uuid.UUID  `json:"id"`
	PeerID         uuid.UUID  `json:"peer_id"`
	MoonID         *uuid.UUID `json:"moon_id,omitempty"`
	TunnelType     string     `json:"tunnel_type"`
	LocalSock      *string    `json:"local_sock,omitempty"`
	RemoteSock     *string    `json:"remote_sock,omitempty"`
	SessionKey     *string    `json:"session_key,omitempty"`
	Active         bool       `json:"active"`
	EstablishedAt  time.Time  `json:"established_at"`
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
	LastActivityAt time.Time  `json:"last_activity_at"`
}
