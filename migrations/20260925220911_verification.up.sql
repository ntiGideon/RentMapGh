-- modify "users" table
ALTER TABLE "users" ALTER COLUMN "phone" DROP NOT NULL, ADD COLUMN "deleted_at" timestamptz NULL, ADD COLUMN "avatar_key" character varying NULL, ADD COLUMN "data_saver" boolean NOT NULL DEFAULT false, ADD COLUMN "notification_prefs" jsonb NULL, ADD COLUMN "identity_verified_at" timestamptz NULL, ADD COLUMN "license_verified_at" timestamptz NULL;
-- create "agent_profiles" table
CREATE TABLE "agent_profiles" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "license_number" character varying NULL, "agency_name" character varying NULL, "service_areas" jsonb NULL, "user_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "agent_profiles_users_agent_profile" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- create index "agent_profiles_user_id_key" to table: "agent_profiles"
CREATE UNIQUE INDEX "agent_profiles_user_id_key" ON "agent_profiles" ("user_id");
-- create "landlord_profiles" table
CREATE TABLE "landlord_profiles" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "display_name" character varying NULL, "bio" character varying NULL, "user_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "landlord_profiles_users_landlord_profile" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- create index "landlord_profiles_user_id_key" to table: "landlord_profiles"
CREATE UNIQUE INDEX "landlord_profiles_user_id_key" ON "landlord_profiles" ("user_id");
-- create "verifications" table
CREATE TABLE "verifications" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "kind" character varying NOT NULL, "status" character varying NOT NULL DEFAULT 'pending', "method" character varying NOT NULL DEFAULT 'manual', "id_number_enc" bytea NULL, "id_number_last4" character varying NULL, "license_number" character varying NULL, "reviewed_by" uuid NULL, "reviewed_at" timestamptz NULL, "decision_reason" character varying NULL, "decision_note" character varying NULL, "evidence_purged_at" timestamptz NULL, "user_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "verifications_users_verifications" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- create index "verification_one_pending" to table: "verifications"
CREATE UNIQUE INDEX "verification_one_pending" ON "verifications" ("user_id", "kind") WHERE ((status)::text = 'pending'::text);
-- create index "verification_status_created_at" to table: "verifications"
CREATE INDEX "verification_status_created_at" ON "verifications" ("status", "created_at");
-- create index "verification_user_id_kind_status" to table: "verifications"
CREATE INDEX "verification_user_id_kind_status" ON "verifications" ("user_id", "kind", "status");
-- create "verification_files" table
CREATE TABLE "verification_files" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "user_id" uuid NOT NULL, "kind" character varying NOT NULL, "storage_key" character varying NOT NULL, "content_type" character varying NOT NULL, "size" bigint NOT NULL, "sha256" bytea NOT NULL, "verification_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "verification_files_verifications_files" FOREIGN KEY ("verification_id") REFERENCES "verifications" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- create index "verificationfile_verification_id" to table: "verification_files"
CREATE INDEX "verificationfile_verification_id" ON "verification_files" ("verification_id");
