package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"

	"rentmapgh/internal/platform/money"
)

// Property is a building or compound. One property has many units (hostel
// rooms, flats in a block, rooms in a compound house).
//
// lat/lng are the exact, private point. Everything public reads approx_* —
// see internal/platform/geo and ProjectRequirement §6.1. The PostGIS column
// approx_geog is generated from approx_* in a hand-written migration.
type Property struct{ ent.Schema }

func (Property) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (Property) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("created_by", uuid.UUID{}).Immutable().Comment("the lister who created it (landlord or agent)"),
		field.UUID("owner_id", uuid.UUID{}).Optional().Nillable().Comment("the landlord, when known"),
		field.String("name").MaxLen(80).Optional().Comment("e.g. \"Adom Hostel\""),
		field.String("category").MaxLen(20).Default("residential"),
		field.Float("lat").Optional().Nillable().Comment("exact, private"),
		field.Float("lng").Optional().Nillable().Comment("exact, private"),
		field.Float("approx_lat").Optional().Nillable(),
		field.Float("approx_lng").Optional().Nillable(),
		field.String("digital_address").MaxLen(14).Optional().Comment("GhanaPostGPS, e.g. AK-039-5028"),
		field.String("landmark").MaxLen(160).Optional().Comment("\"Behind Ayigya Zongo mosque\""),
		field.String("street").MaxLen(120).Optional(),
		field.String("neighbourhood").MaxLen(40).Optional().Comment("slug from geo.Neighbourhoods"),
		field.String("city").MaxLen(40).Default("Kumasi"),
		field.String("region").MaxLen(40).Default("Ashanti"),
	}
}

func (Property) Edges() []ent.Edge {
	return []ent.Edge{edge.To("units", Unit.Type)}
}

func (Property) Indexes() []ent.Index {
	return []ent.Index{index.Fields("created_by"), index.Fields("owner_id")}
}

// Unit is one lettable space in a property.
type Unit struct{ ent.Schema }

func (Unit) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (Unit) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("property_id", uuid.UUID{}).Immutable(),
		field.String("label").MaxLen(40).Optional().Comment("\"Room 4\", \"Flat B2\""),
		field.String("unit_type").MaxLen(30).Optional().Comment("listings.UnitTypes key"),
		field.Int("bedrooms").Optional().Nillable().NonNegative(),
		field.Int("bathrooms").Optional().Nillable().NonNegative(),
		field.Int("size_sqm").Optional().Nillable().NonNegative(),
		field.Int("floor").Optional().Nillable(),
		field.String("furnished").MaxLen(10).Optional(),
		field.Bool("self_contained").Optional().Nillable(),
		field.String("meter_type").MaxLen(20).Optional(),
		field.String("water_source").MaxLen(20).Optional(),
		field.String("kitchen").MaxLen(10).Optional(),
		field.JSON("amenities", []string{}).Optional().Comment("listings.Amenities keys"),
	}
}

func (Unit) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("property", Property.Type).Ref("units").Field("property_id").Unique().Required().Immutable(),
		edge.To("listings", Listing.Type),
	}
}

func (Unit) Indexes() []ent.Index { return []ent.Index{index.Fields("property_id")} }

// Listing is a unit offered for rent by a lister.
type Listing struct{ ent.Schema }

func (Listing) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (Listing) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("unit_id", uuid.UUID{}).Immutable(),
		field.UUID("lister_id", uuid.UUID{}).Immutable(),
		field.Enum("lister_kind").Values("owner", "agent").Immutable(),
		field.Enum("status").Values("draft", "pending_review", "active", "paused", "rented", "expired", "removed").Default("draft"),
		field.String("wizard_step").MaxLen(20).Default("location").Comment("where the lister left off"),
		field.String("headline").MaxLen(90).Optional(),
		field.String("description").MaxLen(3000).Optional(),
		field.Time("available_from").Optional().Nillable(),
		field.Time("last_confirmed_at").Optional().Nillable(),
		field.Int("quality_score").Default(0).Min(0).Max(100),
		field.Int("trust_score").Default(0).Min(0).Max(100),
		field.Int("views_count").Default(0).NonNegative(),
		field.Time("promoted_until").Optional().Nillable(),
		field.Time("submitted_at").Optional().Nillable(),
		field.Time("published_at").Optional().Nillable(),
		field.UUID("reviewed_by", uuid.UUID{}).Optional().Nillable(),
		field.String("review_note").MaxLen(500).Optional().Comment("moderator's note when sending back"),
		// Availability (ProjectRequirement §6.7).
		field.Time("nudged_at").Optional().Nillable().Comment("last \"still available?\" SMS"),
		field.Time("stale_reported_at").Optional().Nillable().Comment("a renter said it's already rented"),
		field.Time("rented_at").Optional().Nillable(),
		field.Enum("rented_via").Values("rentmap", "elsewhere", "unknown").Optional().Nillable().Comment("\"Did you find your tenant through RentMap?\" — the key success metric"),
	}
}

func (Listing) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("unit", Unit.Type).Ref("listings").Field("unit_id").Unique().Required().Immutable(),
		edge.To("terms", ListingTerms.Type).Unique(),
		edge.To("media", ListingMedia.Type),
	}
}

func (Listing) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("lister_id", "status"),
		index.Fields("status", "submitted_at"),
		index.Fields("unit_id"),
	}
}

// ListingTerms is the money side of a listing, in pesewas. Nil means "not
// answered yet" — publishing requires every fee, even if it's zero.
type ListingTerms struct{ ent.Schema }

func (ListingTerms) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func pesewas(name, comment string) ent.Field {
	return field.Int64(name).GoType(money.Pesewas(0)).Optional().Nillable().Min(0).Comment(comment)
}

func (ListingTerms) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("listing_id", uuid.UUID{}).Immutable(),
		pesewas("rent", "per rent_period"),
		field.String("rent_period").MaxLen(20).Default("month"),
		field.Int("advance_periods").Optional().Nillable().Min(0).Max(60),
		pesewas("deposit", ""),
		pesewas("agent_fee", ""),
		pesewas("service_charge", "upfront portion"),
		pesewas("viewing_fee", "paid at the viewing, not part of move-in"),
		field.JSON("other_fees", []money.Fee{}).Optional(),
		pesewas("monthly_equivalent", "derived on save; every price filter uses it"),
		pesewas("move_in_total", "derived on save, for sorting and cards"),
		field.Bool("negotiable").Default(false),
		field.Int("min_lease_months").Optional().Nillable().Min(0).Max(120),
	}
}

func (ListingTerms) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("listing", Listing.Type).Ref("terms").Field("listing_id").Unique().Required().Immutable(),
	}
}

// ListingMedia is one photo or the walk-through video of a listing. Its
// files live in the media store under media/{id}/ (see listings.MediaKey);
// the photo at position 0 is the cover. Only re-encoded renditions are
// stored — never the upload itself, which may carry EXIF/QuickTime GPS
// pointing at the exact building.
type ListingMedia struct{ ent.Schema }

func (ListingMedia) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "listing_media"}}
}

func (ListingMedia) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (ListingMedia) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("listing_id", uuid.UUID{}).Immutable(),
		field.UUID("uploaded_by", uuid.UUID{}).Immutable(),
		field.Enum("kind").Values("photo", "video").Default("photo").Immutable(),
		field.Int("position").Default(0).NonNegative(),
		// Photos are ready on upload; a video is "processing" until the worker
		// has transcoded it (listings.ProcessVideos).
		field.Enum("status").Values("processing", "ready", "failed").Default("ready"),
		field.Int("width").Positive().Comment("of the largest rendition"),
		field.Int("height").Positive(),
		field.Int("bytes").NonNegative().Comment("all renditions together"),
		field.Int("duration_ms").Positive().Optional().Nillable().Immutable().Comment("videos only"),
		field.String("blurhash").MaxLen(40).Optional().Comment("set when a video's poster is ready"),
		field.Int64("phash").Comment("64-bit perceptual hash (imaging.PHash; a video's poster frame), for duplicate detection"),
	}
}

func (ListingMedia) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("listing", Listing.Type).Ref("media").Field("listing_id").Unique().Required().Immutable(),
	}
}

func (ListingMedia) Indexes() []ent.Index {
	return []ent.Index{index.Fields("listing_id", "position"), index.Fields("status")}
}
