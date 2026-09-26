-- reverse: create index "session_user_id_revoked_at" to table: "sessions"
DROP INDEX "session_user_id_revoked_at";
-- reverse: create index "session_token_hash" to table: "sessions"
DROP INDEX "session_token_hash";
-- reverse: create "sessions" table
DROP TABLE "sessions";
-- reverse: create index "roleassignment_user_id_role" to table: "role_assignments"
DROP INDEX "roleassignment_user_id_role";
-- reverse: create "role_assignments" table
DROP TABLE "role_assignments";
-- reverse: create index "user_phone" to table: "users"
DROP INDEX "user_phone";
-- reverse: create "users" table
DROP TABLE "users";
-- reverse: create index "otpcode_phone_created_at" to table: "otp_codes"
DROP INDEX "otpcode_phone_created_at";
-- reverse: create index "otpcode_created_at" to table: "otp_codes"
DROP INDEX "otpcode_created_at";
-- reverse: create "otp_codes" table
DROP TABLE "otp_codes";
-- reverse: create index "auditevent_actor_id_created_at" to table: "audit_events"
DROP INDEX "auditevent_actor_id_created_at";
-- reverse: create index "auditevent_action_created_at" to table: "audit_events"
DROP INDEX "auditevent_action_created_at";
-- reverse: create "audit_events" table
DROP TABLE "audit_events";
