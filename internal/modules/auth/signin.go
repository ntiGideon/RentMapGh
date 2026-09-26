package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/modules/audit"
)

var ErrSuspended = errors.New("This account has been suspended. Please contact support.")

// SignIn finds or creates the user for a phone that just passed OTP and marks
// the phone verified. created reports a brand-new account.
func SignIn(ctx context.Context, db *ent.Client, e164, ip, ua string) (*ent.User, bool, error) {
	now := time.Now().UTC()
	created := false

	u, err := db.User.Query().Where(user.Phone(e164)).Only(ctx)
	switch {
	case ent.IsNotFound(err):
		u, err = db.User.Create().SetPhone(e164).SetPhoneVerifiedAt(now).SetLastSeenAt(now).Save(ctx)
		if ent.IsConstraintError(err) { // another device won the race
			u, err = db.User.Query().Where(user.Phone(e164)).Only(ctx)
		} else {
			created = err == nil
		}
	}
	if err != nil {
		return nil, false, fmt.Errorf("signin: load user: %w", err)
	}

	if u.Status != user.StatusActive {
		audit.New(db).Record(ctx, audit.Event{Actor: &u.ID, Action: audit.LoginBlocked, IP: ip, UserAgent: ua})
		return nil, false, ErrSuspended
	}
	if !created {
		if u, err = db.User.UpdateOne(u).SetPhoneVerifiedAt(now).SetLastSeenAt(now).Save(ctx); err != nil {
			return nil, false, fmt.Errorf("signin: update user: %w", err)
		}
	}
	if u.Edges.Roles, err = u.QueryRoles().All(ctx); err != nil {
		return nil, false, fmt.Errorf("signin: roles: %w", err)
	}

	action := audit.Login
	if created {
		action = audit.SignUp
	}
	audit.New(db).Record(ctx, audit.Event{Actor: &u.ID, Action: action, IP: ip, UserAgent: ua})
	return u, created, nil
}
