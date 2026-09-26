// Package audit records sensitive actions in an append-only table.
package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"rentmapgh/internal/ent"
)

// Action names. Keep them stable: dashboards and alerts key on them.
const (
	OTPSent            = "auth.otp_sent"
	OTPFailed          = "auth.otp_failed"
	OTPLocked          = "auth.otp_locked"
	SignUp             = "auth.signup"
	Login              = "auth.login"
	LoginBlocked       = "auth.login_blocked"
	Logout             = "auth.logout"
	SessionRevoked     = "session.revoked"
	SessionsRevokedAll = "session.revoked_others"
	Onboarded          = "user.onboarded"
	RolesAdded         = "user.roles_added"
	ProfileUpdated     = "user.profile_updated"
)

// Event is one audit record. Actor is nil for anonymous or system actions.
type Event struct {
	Actor      *uuid.UUID
	Action     string
	TargetType string
	TargetID   string
	IP         string
	UserAgent  string
	Meta       map[string]any
}

type Log struct{ db *ent.Client }

func New(db *ent.Client) *Log { return &Log{db: db} }

// Record writes e. Failures are logged, never returned: an audit hiccup must
// not break sign-in. Pass a transactional client via RecordTx when the event
// must commit atomically with the change it describes.
func (l *Log) Record(ctx context.Context, e Event) {
	if err := create(ctx, l.db, e); err != nil {
		slog.ErrorContext(ctx, "audit: record", "action", e.Action, "err", err)
	}
}

// RecordTx writes e inside tx and returns any error so the caller can roll back.
func RecordTx(ctx context.Context, tx *ent.Tx, e Event) error {
	return create(ctx, tx.Client(), e)
}

func create(ctx context.Context, db *ent.Client, e Event) error {
	return db.AuditEvent.Create().
		SetCreatedAt(time.Now().UTC()).
		SetNillableActorID(e.Actor).
		SetAction(e.Action).
		SetTargetType(e.TargetType).
		SetTargetID(e.TargetID).
		SetIP(clip(e.IP, 64)).
		SetUserAgent(clip(e.UserAgent, 300)).
		SetMeta(e.Meta).
		Exec(ctx)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
