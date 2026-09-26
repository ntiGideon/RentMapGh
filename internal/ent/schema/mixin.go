package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
	"github.com/google/uuid"
)

// IDMixin gives every table a sortable UUIDv7 primary key.
type IDMixin struct{ mixin.Schema }

func (IDMixin) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(func() uuid.UUID { return uuid.Must(uuid.NewV7()) }).
			Immutable(),
	}
}

// TimeMixin adds created_at / updated_at (stored in UTC).
type TimeMixin struct{ mixin.Schema }

func (TimeMixin) Fields() []ent.Field {
	now := func() time.Time { return time.Now().UTC() }
	return []ent.Field{
		field.Time("created_at").Default(now).Immutable(),
		field.Time("updated_at").Default(now).UpdateDefault(now),
	}
}
