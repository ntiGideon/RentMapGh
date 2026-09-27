package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// AgentMandate is a landlord's permission for an agent to list a property.
// The agent asks; the landlord answers through a link sent by SMS to their
// phone, without an account. Only an HMAC of the link token is stored.
// An approved mandate lapses at valid_until (see mandates.Effective).
type AgentMandate struct{ ent.Schema }

func (AgentMandate) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (AgentMandate) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("agent_id", uuid.UUID{}).Immutable(),
		field.UUID("property_id", uuid.UUID{}).Immutable(),
		field.String("landlord_phone").MaxLen(16).NotEmpty().Immutable().Comment("E.164, as the agent entered it"),
		field.String("landlord_name").MaxLen(80).Optional().Immutable(),
		field.UUID("granted_by", uuid.UUID{}).Optional().Nillable().Comment("the landlord's account, if one exists for that phone"),
		field.Enum("status").Values("pending", "approved", "declined", "revoked", "cancelled").Default("pending"),
		field.Int("months").Range(1, 24).Default(12).Comment("how long the approval lasts"),
		field.Bytes("token_hash").NotEmpty().Sensitive().Unique(),
		field.Time("sent_at").Comment("last SMS; a resend rotates the token"),
		field.Int("sends").Default(1).Positive(),
		field.Time("decided_at").Optional().Nillable(),
		field.Time("valid_until").Optional().Nillable(),
		field.Bool("reported").Default(false).Comment("the landlord said they don't know this agent"),
	}
}

func (AgentMandate) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("agent_id", "property_id"),
		index.Fields("agent_id", "created_at"),
		index.Fields("property_id", "status"),
	}
}
