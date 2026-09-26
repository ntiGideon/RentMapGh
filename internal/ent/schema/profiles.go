package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// LandlordProfile is the public face of a property owner.
type LandlordProfile struct{ ent.Schema }

func (LandlordProfile) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (LandlordProfile) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}).Immutable(),
		field.String("display_name").MaxLen(80).Optional().Comment("e.g. \"Mrs Owusu\" or \"Adom Properties\""),
		field.String("bio").MaxLen(600).Optional(),
	}
}

func (LandlordProfile) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("landlord_profile").Field("user_id").Unique().Required().Immutable(),
	}
}

// AgentProfile holds an agent's licence and working areas. Agencies become
// their own entity when team accounts arrive; until then it's a name.
type AgentProfile struct{ ent.Schema }

func (AgentProfile) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (AgentProfile) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}).Immutable(),
		field.String("license_number").MaxLen(40).Optional().Comment("Real Estate Agency Council (Act 1047)"),
		field.String("agency_name").MaxLen(120).Optional(),
		field.JSON("service_areas", []string{}).Optional().Comment("neighbourhood slugs"),
	}
}

func (AgentProfile) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("agent_profile").Field("user_id").Unique().Required().Immutable(),
	}
}
