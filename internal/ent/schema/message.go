package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Conversation is one renter talking to one lister about one listing
// (ProjectRequirement §6.9). Each side's read time drives read receipts and
// unread badges.
type Conversation struct{ ent.Schema }

func (Conversation) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (Conversation) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("listing_id", uuid.UUID{}).Immutable(),
		field.UUID("renter_id", uuid.UUID{}).Immutable(),
		field.UUID("lister_id", uuid.UUID{}).Immutable(),
		field.Time("last_message_at").Optional().Nillable(),
		field.UUID("last_sender_id", uuid.UUID{}).Optional().Nillable(),
		field.Time("renter_read_at").Optional().Nillable(),
		field.Time("lister_read_at").Optional().Nillable(),
		field.Time("renter_notified_at").Optional().Nillable().Comment("last SMS about unread messages"),
		field.Time("lister_notified_at").Optional().Nillable(),
	}
}

func (Conversation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("listing_id", "renter_id").Unique(),
		index.Fields("renter_id", "last_message_at"),
		index.Fields("lister_id", "last_message_at"),
	}
}

// Message is one message in a conversation. Flags are the scam-shield rules
// it tripped (messages.Rules keys); the body is stored as written and
// masked when shown.
type Message struct{ ent.Schema }

func (Message) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (Message) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("conversation_id", uuid.UUID{}).Immutable(),
		field.UUID("sender_id", uuid.UUID{}).Immutable(),
		field.String("body").MaxLen(2000).NotEmpty().Immutable(),
		field.JSON("flags", []string{}).Optional(),
		field.Time("flags_reviewed_at").Optional().Nillable().Comment("a moderator looked at the scam-shield flags"),
	}
}

func (Message) Indexes() []ent.Index {
	return []ent.Index{index.Fields("conversation_id", "created_at")}
}

// Report is a user flagging a message (and later a listing or a user) for
// moderators.
type Report struct{ ent.Schema }

func (Report) Mixin() []ent.Mixin { return []ent.Mixin{IDMixin{}, TimeMixin{}} }

func (Report) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("reporter_id", uuid.UUID{}).Immutable(),
		field.Enum("target_type").Values("message", "listing", "user").Immutable(),
		field.UUID("target_id", uuid.UUID{}).Immutable(),
		field.UUID("subject_id", uuid.UUID{}).Optional().Nillable().Immutable().Comment("the user being reported"),
		field.String("reason").MaxLen(30).Immutable(),
		field.String("note").MaxLen(500).Optional().Immutable(),
		field.Enum("status").Values("open", "actioned", "dismissed").Default("open"),
		field.UUID("handled_by", uuid.UUID{}).Optional().Nillable(),
		field.Time("handled_at").Optional().Nillable(),
	}
}

func (Report) Indexes() []ent.Index {
	return []ent.Index{index.Fields("status", "created_at"), index.Fields("target_type", "target_id"), index.Fields("subject_id")}
}
