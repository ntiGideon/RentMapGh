package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Notification is one item in a user's notification centre. The same event
// may also go out by SMS, depending on the user's preferences (notify).
type Notification struct{ ent.Schema }

func (Notification) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (Notification) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}).Immutable(),
		field.String("topic").MaxLen(20).Immutable().Comment("preference topic: viewings, messages, listings, …"),
		field.String("kind").MaxLen(40).Immutable().Comment("e.g. viewing.confirmed"),
		field.String("title").MaxLen(140).Immutable(),
		field.String("body").MaxLen(300).Optional().Immutable(),
		field.String("url").MaxLen(300).Optional().Immutable(),
		field.Time("read_at").Optional().Nillable(),
	}
}

func (Notification) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "created_at"), index.Fields("user_id", "read_at")}
}
