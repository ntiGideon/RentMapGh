// Command admin performs back-office tasks that have no UI yet.
//
//	admin grant  <phone> <role>   give a role (e.g. moderator, admin) to an existing user
//	admin revoke <phone> <role>   take it away again
//	admin roles  <phone>          list a user's roles
//
// The user must have signed in once. Needs DATABASE_URL.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"rentmapgh/internal/db"
	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/roleassignment"
	"rentmapgh/internal/ent/session"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/platform/phone"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "admin:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: admin grant|revoke <phone> <role> | admin roles <phone>")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, err := db.Open(ctx, url, 2)
	if err != nil {
		return err
	}
	defer d.Close()

	e164, err := phone.NormalizeGhana(args[1])
	if err != nil {
		return fmt.Errorf("%s: %w", args[1], err)
	}
	u, err := d.Ent.User.Query().Where(user.Phone(e164), user.StatusEQ(user.StatusActive)).WithRoles().Only(ctx)
	if ent.IsNotFound(err) {
		return fmt.Errorf("no active user with phone %s — they need to sign in once first", phone.Mask(e164))
	}
	if err != nil {
		return err
	}

	switch args[0] {
	case "roles":
		fmt.Println(strings.Join(roleNames(u), ", "))
		return nil
	case "grant", "revoke":
		if len(args) < 3 {
			return fmt.Errorf("usage: admin %s <phone> <role>", args[0])
		}
		role := roleassignment.Role(args[2])
		if err := roleassignment.RoleValidator(role); err != nil {
			return fmt.Errorf("unknown role %q (renter, landlord, agent, field_verifier, moderator, admin)", args[2])
		}
		if args[0] == "grant" {
			err = d.Ent.RoleAssignment.Create().SetUserID(u.ID).SetRole(role).Exec(ctx)
			if ent.IsConstraintError(err) {
				fmt.Printf("%s already has %s\n", phone.Mask(e164), role)
				return nil
			}
		} else {
			_, err = d.Ent.RoleAssignment.Delete().Where(roleassignment.UserID(u.ID), roleassignment.RoleEQ(role)).Exec(ctx)
		}
		if err != nil {
			return err
		}
		// Privilege changed: sign the user out everywhere so no old session
		// carries (or lacks) the role.
		if _, err := d.Ent.Session.Update().Where(session.UserID(u.ID), session.RevokedAtIsNil()).
			SetRevokedAt(time.Now().UTC()).Save(ctx); err != nil {
			return err
		}
		audit.New(d.Ent).Record(ctx, audit.Event{Action: "admin.role_" + args[0], TargetType: "user", TargetID: u.ID.String(),
			Meta: map[string]any{"role": string(role), "source": "cli"}})
		fmt.Printf("%sed %s for %s (signed out of all devices)\n", strings.TrimSuffix(args[0], "e"), role, phone.Mask(e164))
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func roleNames(u *ent.User) []string {
	var out []string
	for _, r := range u.Edges.Roles {
		out = append(out, string(r.Role))
	}
	if len(out) == 0 {
		return []string{"(none)"}
	}
	return out
}
