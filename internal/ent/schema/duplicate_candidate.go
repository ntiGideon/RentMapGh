package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// DuplicateCandidate is a pair of listings that look like the same place
// (§6.11): close together with near-identical photos, or the same unit
// type and rent at the same spot by different listers. listing_a < listing_b.
type DuplicateCandidate struct{ ent.Schema }

func (DuplicateCandidate) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (DuplicateCandidate) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("listing_a", uuid.UUID{}).Immutable(),
		field.UUID("listing_b", uuid.UUID{}).Immutable(),
		field.Int("distance_m").NonNegative().Comment("between the exact pins"),
		field.Int("photo_bits").Optional().Nillable().Comment("smallest pHash Hamming distance between their photos; nil when either has none"),
		field.Float("text_similarity").Default(0).Comment("pg_trgm similarity of the headlines"),
		field.Int("score"),
		field.Enum("status").Values("open", "dismissed", "actioned").Default("open"),
		field.UUID("handled_by", uuid.UUID{}).Optional().Nillable(),
		field.Time("handled_at").Optional().Nillable(),
	}
}

func (DuplicateCandidate) Indexes() []ent.Index {
	return []ent.Index{index.Fields("listing_a", "listing_b").Unique(), index.Fields("status", "score")}
}
