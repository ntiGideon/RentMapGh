-- reverse: create index "report_target_type_target_id" to table: "reports"
DROP INDEX "report_target_type_target_id";
-- reverse: create index "report_subject_id" to table: "reports"
DROP INDEX "report_subject_id";
-- reverse: create index "report_status_created_at" to table: "reports"
DROP INDEX "report_status_created_at";
-- reverse: create "reports" table
DROP TABLE "reports";
-- reverse: create index "message_conversation_id_created_at" to table: "messages"
DROP INDEX "message_conversation_id_created_at";
-- reverse: create "messages" table
DROP TABLE "messages";
-- reverse: create index "conversation_renter_id_last_message_at" to table: "conversations"
DROP INDEX "conversation_renter_id_last_message_at";
-- reverse: create index "conversation_listing_id_renter_id" to table: "conversations"
DROP INDEX "conversation_listing_id_renter_id";
-- reverse: create index "conversation_lister_id_last_message_at" to table: "conversations"
DROP INDEX "conversation_lister_id_last_message_at";
-- reverse: create "conversations" table
DROP TABLE "conversations";
