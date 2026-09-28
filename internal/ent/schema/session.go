package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Session is one signed-in device. Only a SHA-256 of the cookie token is
// stored, so a database leak doesn't hand out live sessions.
type Session struct{ ent.Schema }

func (Session) Mixin() []ent.Mixin {
	return []ent.Mixin{IDMixin{}, TimeMixin{}}
}

func (Session) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("token_hash").NotEmpty().Sensitive(),
		field.UUID("user_id", uuid.UUID{}).Immutable(),
		field.String("user_agent").MaxLen(300).Optional(),
		field.String("ip").MaxLen(64).Optional(),
		field.Time("last_seen_at"),
		field.Time("expires_at"),
		field.Time("revoked_at").Optional().Nillable(),
		field.UUID("impersonator_id", uuid.UUID{}).Optional().Nillable().Immutable().
			Comment("the admin viewing as this user (support); such sessions are read-only and short"),
	}
}

func (Session) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("sessions").Field("user_id").Unique().Required().Immutable(),
	}
}

func (Session) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("token_hash").Unique(),
		index.Fields("user_id", "revoked_at"),
	}
}
