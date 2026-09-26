package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// OTPCode is a one-time sign-in code sent by SMS. The code itself is never
// stored: code_hash is an HMAC of phone + code keyed with AUTH_SECRET. Rows
// double as the send log used for per-phone and global rate limits.
type OTPCode struct{ ent.Schema }

func (OTPCode) Mixin() []ent.Mixin {
	return []ent.Mixin{IDMixin{}, TimeMixin{}}
}

func (OTPCode) Fields() []ent.Field {
	return []ent.Field{
		field.String("phone").NotEmpty().MaxLen(16),
		field.Bytes("code_hash").NotEmpty().Sensitive(),
		field.Int("attempts").Default(0).NonNegative(),
		field.Time("expires_at"),
		field.Time("consumed_at").Optional().Nillable().Comment("used, superseded or burnt by too many attempts"),
		field.String("channel").MaxLen(20).Default("sms"),
		field.String("ip").MaxLen(64).Optional(),
	}
}

func (OTPCode) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("phone", "created_at"),
		index.Fields("created_at"),
	}
}
