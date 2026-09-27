package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"

	"rentmapgh/internal/platform/money"
)

// Viewing is a renter's request to see a listing, and its outcome
// (ProjectRequirement §6.8). The exact location and both phone numbers are
// shared only while it is confirmed.
//
//	requested ─ accept ──────────────► confirmed ─► completed / no_show
//	    │     └ propose ─► proposed ─ renter accepts ┘
//	    └ decline / cancel (either side, any open state) ─► declined / cancelled
type Viewing struct{ ent.Schema }

func (Viewing) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (Viewing) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("listing_id", uuid.UUID{}).Immutable(),
		field.UUID("renter_id", uuid.UUID{}).Immutable(),
		field.UUID("lister_id", uuid.UUID{}).Immutable(),
		field.Enum("status").Values("requested", "proposed", "confirmed", "declined", "cancelled", "completed", "no_show").Default("requested"),
		field.Time("starts_at").Comment("the requested time, or the lister's proposal while proposed"),
		field.Int("duration_min").Default(30).Range(15, 120),
		field.String("note").MaxLen(300).Optional().Comment("renter's message with the request"),
		field.Int64("viewing_fee").GoType(money.Pesewas(0)).Optional().Nillable().Comment("snapshot at request time"),
		field.Bool("fee_acknowledged").Default(false),
		field.String("decline_reason").MaxLen(40).Optional(),
		field.UUID("closed_by", uuid.UUID{}).Optional().Nillable().Comment("who declined or cancelled"),
		field.Time("confirmed_at").Optional().Nillable(),
		field.Time("location_seen_at").Optional().Nillable().Comment("first time the renter opened the exact location"),
		field.Time("responded_at").Optional().Nillable().Comment("the lister's first answer (response-time badge)"),
		// Reminders and feedback (ProjectRequirement §6.8).
		field.Time("reminded_24_at").Optional().Nillable(),
		field.Time("reminded_2_at").Optional().Nillable(),
		field.Time("feedback_asked_at").Optional().Nillable(),
		field.Enum("renter_outcome").Values("happened", "renter_missed", "lister_missed").Optional().Nillable(),
		field.Enum("accuracy").Values("as_described", "mostly", "not_as_described").Optional().Nillable(),
		field.Bool("interested").Optional().Nillable(),
		field.String("feedback_note").MaxLen(500).Optional(),
		field.Time("feedback_at").Optional().Nillable(),
	}
}

func (Viewing) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("renter_id", "starts_at"),
		index.Fields("lister_id", "starts_at"),
		index.Fields("listing_id", "status"),
		index.Fields("status", "starts_at"),
	}
}

// SavedListing is a signed-in renter's saved place. Anonymous saves live in
// a cookie and move here on the first request after sign-in.
type SavedListing struct{ ent.Schema }

func (SavedListing) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (SavedListing) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}).Immutable(),
		field.UUID("listing_id", uuid.UUID{}).Immutable(),
	}
}

func (SavedListing) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "listing_id").Unique(), index.Fields("user_id", "created_at")}
}
