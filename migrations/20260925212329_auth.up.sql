-- create "audit_events" table
CREATE TABLE "audit_events" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "actor_id" uuid NULL, "action" character varying NOT NULL, "target_type" character varying NULL, "target_id" character varying NULL, "ip" character varying NULL, "user_agent" character varying NULL, "meta" jsonb NULL, PRIMARY KEY ("id"));
-- create index "auditevent_action_created_at" to table: "audit_events"
CREATE INDEX "auditevent_action_created_at" ON "audit_events" ("action", "created_at");
-- create index "auditevent_actor_id_created_at" to table: "audit_events"
CREATE INDEX "auditevent_actor_id_created_at" ON "audit_events" ("actor_id", "created_at");
-- create "otp_codes" table
CREATE TABLE "otp_codes" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "phone" character varying NOT NULL, "code_hash" bytea NOT NULL, "attempts" bigint NOT NULL DEFAULT 0, "expires_at" timestamptz NOT NULL, "consumed_at" timestamptz NULL, "channel" character varying NOT NULL DEFAULT 'sms', "ip" character varying NULL, PRIMARY KEY ("id"));
-- create index "otpcode_created_at" to table: "otp_codes"
CREATE INDEX "otpcode_created_at" ON "otp_codes" ("created_at");
-- create index "otpcode_phone_created_at" to table: "otp_codes"
CREATE INDEX "otpcode_phone_created_at" ON "otp_codes" ("phone", "created_at");
-- create "users" table
CREATE TABLE "users" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "phone" character varying NOT NULL, "name" character varying NULL, "status" character varying NOT NULL DEFAULT 'active', "phone_verified_at" timestamptz NULL, "onboarded_at" timestamptz NULL, "last_seen_at" timestamptz NULL, PRIMARY KEY ("id"));
-- create index "user_phone" to table: "users"
CREATE UNIQUE INDEX "user_phone" ON "users" ("phone");
-- create "role_assignments" table
CREATE TABLE "role_assignments" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "role" character varying NOT NULL, "granted_by" uuid NULL, "user_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "role_assignments_users_roles" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- create index "roleassignment_user_id_role" to table: "role_assignments"
CREATE UNIQUE INDEX "roleassignment_user_id_role" ON "role_assignments" ("user_id", "role");
-- create "sessions" table
CREATE TABLE "sessions" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "token_hash" bytea NOT NULL, "user_agent" character varying NULL, "ip" character varying NULL, "last_seen_at" timestamptz NOT NULL, "expires_at" timestamptz NOT NULL, "revoked_at" timestamptz NULL, "user_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "sessions_users_sessions" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- create index "session_token_hash" to table: "sessions"
CREATE UNIQUE INDEX "session_token_hash" ON "sessions" ("token_hash");
-- create index "session_user_id_revoked_at" to table: "sessions"
CREATE INDEX "session_user_id_revoked_at" ON "sessions" ("user_id", "revoked_at");
