package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// AuditEvent is an append-only record of a sensitive action.
type AuditEvent struct{ ent.Schema }

func (AuditEvent) Mixin() []ent.Mixin {
	return []ent.Mixin{IDMixin{}}
}

func (AuditEvent) Fields() []ent.Field {
	return []ent.Field{
		field.Time("created_at").Immutable(),
		field.UUID("actor_id", uuid.UUID{}).Optional().Nillable().Immutable().Comment("nil for anonymous/system"),
		field.String("action").NotEmpty().MaxLen(60).Immutable().Comment("e.g. auth.login, session.revoked"),
		field.String("target_type").MaxLen(40).Optional().Immutable(),
		field.String("target_id").MaxLen(64).Optional().Immutable(),
		field.String("ip").MaxLen(64).Optional().Immutable(),
		field.String("user_agent").MaxLen(300).Optional().Immutable(),
		field.JSON("meta", map[string]any{}).Optional().Immutable(),
	}
}

func (AuditEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("actor_id", "created_at"),
		index.Fields("action", "created_at"),
	}
}
