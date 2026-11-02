// Package organization owns organization/territory trees and delegated staff
// authority. Authority is resolved from current rows on every operation.
package organization

import (
	"errors"
	"time"
)

var (
	ErrForbidden = errors.New("organization access forbidden")
	ErrInvalid   = errors.New("invalid organization request")
	ErrConflict  = errors.New("organization revision conflict")
	ErrNotFound  = errors.New("organization resource not found")
)

const (
	Read            = "organization.read"
	Manage          = "organization.manage"
	StaffManage     = "staff.manage"
	ResourceRead    = "resource.read"
	ResourceExport  = "resource.export"
	ResourceCommand = "resource.command"
)

// PlatformAdmin is set only by the authenticated BFF, never decoded from JSON.
type Actor struct {
	TenantID          string
	Country           string
	Subject           string
	PlatformAdmin     bool
	MFA               bool
	OperatorSessionID string
	OperatorRevision  int64
	// Set by the authenticated HTTP boundary, never decoded from a request.
	// Trusted in-process bootstrap fixtures do not represent operator sessions.
	RequireLiveOperator bool
}

type Node struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Active   bool   `json:"active"`
	Revision int64  `json:"revision"`
}
type JobRole struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
}
type Assignment struct {
	Subject  string `json:"subject_id"`
	NodeID   string `json:"node_id"`
	RoleID   string `json:"role_id"`
	Status   string `json:"status"`
	Revision int64  `json:"revision"`
}
type Invitation struct {
	ID        string    `json:"id"`
	Token     string    `json:"token,omitempty"`
	Subject   string    `json:"subject_id"`
	NodeID    string    `json:"node_id"`
	RoleID    string    `json:"role_id"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Workspace struct {
	Nodes []Node       `json:"nodes"`
	Roles []JobRole    `json:"roles"`
	Staff []Assignment `json:"staff"`
}
