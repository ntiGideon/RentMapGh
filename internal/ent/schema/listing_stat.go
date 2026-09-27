package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ListingStat is one listing's counters for one day (Africa/Accra = UTC):
// public page views, saves and contacts (first messages + viewing
// requests). It feeds the lister dashboard and platform metrics.
type ListingStat struct{ ent.Schema }

func (ListingStat) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}} }

func (ListingStat) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("listing_id", uuid.UUID{}).Immutable(),
		field.Time("day").Immutable().Comment("midnight UTC of the day"),
		field.Int("views").Default(0).NonNegative(),
		field.Int("saves").Default(0).NonNegative(),
		field.Int("contacts").Default(0).NonNegative(),
	}
}

func (ListingStat) Indexes() []ent.Index {
	return []ent.Index{index.Fields("listing_id", "day").Unique(), index.Fields("day")}
}
