package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// RoleAssignment gives a user one role; a user can hold several.
type RoleAssignment struct{ ent.Schema }

func (RoleAssignment) Mixin() []ent.Mixin {
	return []ent.Mixin{IDMixin{}, TimeMixin{}}
}

func (RoleAssignment) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}).Immutable(),
		field.Enum("role").Values("renter", "landlord", "agent", "field_verifier", "moderator", "admin").Immutable(),
		field.UUID("granted_by", uuid.UUID{}).Optional().Nillable().Immutable().Comment("nil when self-selected at onboarding"),
	}
}

func (RoleAssignment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("roles").Field("user_id").Unique().Required().Immutable(),
	}
}

func (RoleAssignment) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "role").Unique()}
}
