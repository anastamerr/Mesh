// Package relay provides an authenticated, bounded HTTP CONNECT rendezvous.
// The relay copies an opaque byte stream and does not terminate the protocol
// carried inside it (for example, HTTPS spoken directly by two Mesh peers).
package relay

import (
	"context"
	"errors"
	"time"
)

type Role string

const (
	RoleDevice   Role = "device"
	RoleConsumer Role = "consumer"
)

var (
	ErrDenied      = errors.New("relay authorization denied")
	ErrUnavailable = errors.New("relay unavailable")
	errIdle        = errors.New("relay waiter renewed")
)

type AuthRequest struct {
	Role   Role
	NodeID string
	Bearer string
}

type Lease struct {
	Subject   string
	ExpiresAt time.Time
}

// Authorizer is called before a connection is accepted and periodically while
// it is queued or active. Revalidate must fail closed when its authority is
// unavailable. Implementations receive the original bearer only in memory.
type Authorizer interface {
	Authorize(context.Context, AuthRequest) (Lease, error)
	Revalidate(context.Context, AuthRequest, Lease) (Lease, error)
}

func validRole(role Role) bool { return role == RoleDevice || role == RoleConsumer }
