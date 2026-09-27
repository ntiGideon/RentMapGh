-- create "conversations" table
CREATE TABLE "conversations" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "listing_id" uuid NOT NULL, "renter_id" uuid NOT NULL, "lister_id" uuid NOT NULL, "last_message_at" timestamptz NULL, "last_sender_id" uuid NULL, "renter_read_at" timestamptz NULL, "lister_read_at" timestamptz NULL, "renter_notified_at" timestamptz NULL, "lister_notified_at" timestamptz NULL, PRIMARY KEY ("id"));
-- create index "conversation_lister_id_last_message_at" to table: "conversations"
CREATE INDEX "conversation_lister_id_last_message_at" ON "conversations" ("lister_id", "last_message_at");
-- create index "conversation_listing_id_renter_id" to table: "conversations"
CREATE UNIQUE INDEX "conversation_listing_id_renter_id" ON "conversations" ("listing_id", "renter_id");
-- create index "conversation_renter_id_last_message_at" to table: "conversations"
CREATE INDEX "conversation_renter_id_last_message_at" ON "conversations" ("renter_id", "last_message_at");
-- create "messages" table
CREATE TABLE "messages" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "conversation_id" uuid NOT NULL, "sender_id" uuid NOT NULL, "body" character varying NOT NULL, "flags" jsonb NULL, PRIMARY KEY ("id"));
-- create index "message_conversation_id_created_at" to table: "messages"
CREATE INDEX "message_conversation_id_created_at" ON "messages" ("conversation_id", "created_at");
-- create "reports" table
CREATE TABLE "reports" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "reporter_id" uuid NOT NULL, "target_type" character varying NOT NULL, "target_id" uuid NOT NULL, "subject_id" uuid NULL, "reason" character varying NOT NULL, "note" character varying NULL, "status" character varying NOT NULL DEFAULT 'open', "handled_by" uuid NULL, "handled_at" timestamptz NULL, PRIMARY KEY ("id"));
-- create index "report_status_created_at" to table: "reports"
CREATE INDEX "report_status_created_at" ON "reports" ("status", "created_at");
-- create index "report_subject_id" to table: "reports"
CREATE INDEX "report_subject_id" ON "reports" ("subject_id");
-- create index "report_target_type_target_id" to table: "reports"
CREATE INDEX "report_target_type_target_id" ON "reports" ("target_type", "target_id");
