package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// WaitlistEntry is a pre-launch sign-up from the "coming soon" page.
type WaitlistEntry struct{ ent.Schema }

func (WaitlistEntry) Mixin() []ent.Mixin {
	return []ent.Mixin{IDMixin{}, TimeMixin{}}
}

func (WaitlistEntry) Fields() []ent.Field {
	return []ent.Field{
		field.String("phone").NotEmpty().MaxLen(16).Comment("E.164, e.g. +233241234567"),
		field.String("name").MaxLen(80).Optional(),
		field.Enum("role").Values("renter", "landlord", "agent").Default("renter"),
		field.String("area").MaxLen(60).Optional().Comment("neighbourhood slug of interest"),
		field.String("source").MaxLen(40).Optional().Comment("utm_source / referral code"),
		field.Bool("consent").Default(false).Comment("agreed to be contacted (Act 843)"),
	}
}

func (WaitlistEntry) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("phone").Unique(),
		index.Fields("role", "area"),
	}
}
