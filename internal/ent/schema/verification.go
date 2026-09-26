package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"

	"rentmapgh/internal/ent/privacy"
	"rentmapgh/internal/ent/rule"
)

// Verification is one request to verify something about a user: their
// identity (Ghana Card + selfie) or their agent licence. Phone verification
// is implicit in OTP sign-in (User.phone_verified_at).
type Verification struct{ ent.Schema }

func (Verification) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (Verification) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}).Immutable(),
		field.Enum("kind").Values("identity", "license").Immutable(),
		field.Enum("status").Values("pending", "approved", "rejected", "withdrawn").Default("pending"),
		field.String("method").MaxLen(30).Default("manual").Comment("manual review now; e.g. smile_id later"),
		field.Bytes("id_number_enc").Optional().Nillable().Sensitive().Comment("Ghana Card PIN, AES-GCM sealed"),
		field.String("id_number_last4").MaxLen(4).Optional().Comment("for lists and audit, never the full PIN"),
		field.String("license_number").MaxLen(40).Optional(),
		field.UUID("reviewed_by", uuid.UUID{}).Optional().Nillable(),
		field.Time("reviewed_at").Optional().Nillable(),
		field.String("decision_reason").MaxLen(40).Optional().Comment("reason code, see verification.Reasons"),
		field.String("decision_note").MaxLen(500).Optional().Comment("shown to the user"),
		field.Time("evidence_purged_at").Optional().Nillable(),
	}
}

func (Verification) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("verifications").Field("user_id").Unique().Required().Immutable(),
		edge.To("files", VerificationFile.Type),
	}
}

func (Verification) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "kind", "status"),
		index.Fields("status", "created_at"),
		// At most one open review per user and kind.
		index.Fields("user_id", "kind").Unique().StorageKey("verification_one_pending").
			Annotations(entsql.IndexWhere("status = 'pending'")),
	}
}

// Policy: users see and create only their own; moderators/admins see all and
// record decisions. System jobs use privacy.DecisionContext.
func (Verification) Policy() ent.Policy {
	return privacy.Policy{
		Mutation: privacy.MutationPolicy{rule.AllowStaff(), rule.AllowOwnerCreate(), privacy.AlwaysDenyRule()},
		Query:    privacy.QueryPolicy{rule.AllowStaff(), rule.FilterToOwner()},
	}
}

// VerificationFile is one piece of evidence (encrypted in storage).
type VerificationFile struct{ ent.Schema }

func (VerificationFile) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (VerificationFile) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("verification_id", uuid.UUID{}).Immutable(),
		field.UUID("user_id", uuid.UUID{}).Immutable().Comment("owner, denormalised for the privacy filter"),
		field.Enum("kind").Values("id_front", "id_back", "selfie", "license_doc").Immutable(),
		field.String("storage_key").MaxLen(200).Immutable(),
		field.String("content_type").MaxLen(60).Immutable(),
		field.Int("size").NonNegative().Immutable(),
		field.Bytes("sha256").Immutable(),
	}
}

func (VerificationFile) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("verification", Verification.Type).Ref("files").Field("verification_id").Unique().Required().Immutable(),
	}
}

func (VerificationFile) Indexes() []ent.Index {
	return []ent.Index{index.Fields("verification_id")}
}

func (VerificationFile) Policy() ent.Policy {
	return privacy.Policy{
		Mutation: privacy.MutationPolicy{rule.AllowStaff(), rule.AllowOwnerCreate(), privacy.AlwaysDenyRule()},
		Query:    privacy.QueryPolicy{rule.AllowStaff(), rule.FilterToOwner()},
	}
}
