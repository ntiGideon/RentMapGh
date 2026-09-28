package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"rentmapgh/internal/platform/weekly"
)

// User is anyone with an account. The phone number is the login identity.
type User struct{ ent.Schema }

func (User) Mixin() []ent.Mixin {
	return []ent.Mixin{IDMixin{}, TimeMixin{}}
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("phone").NotEmpty().MaxLen(16).Optional().Nillable().
			Comment("E.164, e.g. +233241234567; NULL once the account is deleted, freeing the number"),
		field.String("name").MaxLen(80).Optional(),
		field.Enum("status").Values("active", "suspended", "deleted").Default("active"),
		field.Time("phone_verified_at").Optional().Nillable().Comment("set on every successful OTP sign-in"),
		field.Time("onboarded_at").Optional().Nillable().Comment("role selection finished"),
		field.Time("last_seen_at").Optional().Nillable(),
		field.Time("deleted_at").Optional().Nillable(),
		field.Time("suspended_at").Optional().Nillable(),
		field.String("suspension_note").MaxLen(500).Optional().Comment("why (staff only)"),
		field.String("avatar_key").MaxLen(200).Optional().Comment("storage key of the profile photo"),
		field.Bool("data_saver").Default(false),
		field.JSON("notification_prefs", map[string]bool{}).Optional().Comment("\"<topic>.<channel>\" → on/off"),
		field.Time("identity_verified_at").Optional().Nillable().Comment("denormalised from the approved identity Verification"),
		field.Time("license_verified_at").Optional().Nillable().Comment("denormalised from the approved licence Verification"),
		field.JSON("viewing_hours", []weekly.Window{}).Optional().Comment("when this lister shows places, in Africa/Accra time"),
	}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("roles", RoleAssignment.Type),
		edge.To("sessions", Session.Type),
		edge.To("landlord_profile", LandlordProfile.Type).Unique(),
		edge.To("agent_profile", AgentProfile.Type).Unique(),
		edge.To("verifications", Verification.Type),
	}
}

func (User) Indexes() []ent.Index {
	return []ent.Index{index.Fields("phone").Unique()}
}
