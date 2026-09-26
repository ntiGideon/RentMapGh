// Package rule holds the Ent privacy rules shared by sensitive schemas. They
// read the signed-in viewer from the request context (reqctx), so a handler
// that forgets a WHERE user_id = … still can't leak another user's rows.
package rule

import (
	"context"

	"entgo.io/ent"
	"entgo.io/ent/entql"
	"github.com/google/uuid"

	"rentmapgh/internal/ent/privacy"
	"rentmapgh/internal/server/reqctx"
)

// StaffRoles may read and decide on any user's verification.
var StaffRoles = []string{"moderator", "admin"}

// AllowStaff allows everything for moderators and admins.
func AllowStaff() privacy.QueryMutationRule {
	return privacy.ContextQueryMutationRule(func(ctx context.Context) error {
		if reqctx.CurrentViewer(ctx).HasAny(StaffRoles...) {
			return privacy.Allow
		}
		return privacy.Skip
	})
}

// AllowOwnerCreate lets a signed-in user create rows that belong to them.
func AllowOwnerCreate() privacy.MutationRule {
	return privacy.MutationRuleFunc(func(ctx context.Context, m ent.Mutation) error {
		v := reqctx.CurrentViewer(ctx)
		if v == nil || !m.Op().Is(ent.OpCreate) {
			return privacy.Skip
		}
		owned, ok := m.(interface{ UserID() (uuid.UUID, bool) })
		if !ok {
			return privacy.Skip
		}
		if id, set := owned.UserID(); set && id == v.UserID {
			return privacy.Allow
		}
		return privacy.Skip
	})
}

// FilterToOwner restricts queries to the viewer's own rows, and denies
// anonymous queries outright.
func FilterToOwner() privacy.QueryRule {
	return privacy.FilterFunc(func(ctx context.Context, f privacy.Filter) error {
		v := reqctx.CurrentViewer(ctx)
		if v == nil {
			return privacy.Denyf("rule: no viewer in context")
		}
		owned, ok := f.(interface{ WhereUserID(entql.ValueP) })
		if !ok {
			return privacy.Denyf("rule: %T has no user_id", f)
		}
		owned.WhereUserID(entql.ValueEQ(v.UserID))
		return privacy.Skip
	})
}
