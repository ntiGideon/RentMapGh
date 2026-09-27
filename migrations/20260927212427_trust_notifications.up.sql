-- modify "viewings" table
ALTER TABLE "viewings" ADD COLUMN "responded_at" timestamptz NULL, ADD COLUMN "reminded_24_at" timestamptz NULL, ADD COLUMN "reminded_2_at" timestamptz NULL, ADD COLUMN "feedback_asked_at" timestamptz NULL, ADD COLUMN "renter_outcome" character varying NULL, ADD COLUMN "accuracy" character varying NULL, ADD COLUMN "interested" boolean NULL, ADD COLUMN "feedback_note" character varying NULL, ADD COLUMN "feedback_at" timestamptz NULL;
-- create "notifications" table
CREATE TABLE "notifications" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "user_id" uuid NOT NULL, "topic" character varying NOT NULL, "kind" character varying NOT NULL, "title" character varying NOT NULL, "body" character varying NULL, "url" character varying NULL, "read_at" timestamptz NULL, PRIMARY KEY ("id"));
-- create index "notification_user_id_created_at" to table: "notifications"
CREATE INDEX "notification_user_id_created_at" ON "notifications" ("user_id", "created_at");
-- create index "notification_user_id_read_at" to table: "notifications"
CREATE INDEX "notification_user_id_read_at" ON "notifications" ("user_id", "read_at");
