package domain

import "time"

type User struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string
	CreatedAt    time.Time
}

type Session struct {
	TokenHash string
	UserID    string
	CSRFHash  string
	ExpiresAt time.Time
	CreatedAt time.Time
}

const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
	RoleAdmin    = "admin"
)
