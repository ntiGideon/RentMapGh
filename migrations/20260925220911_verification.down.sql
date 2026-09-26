-- reverse: create index "verificationfile_verification_id" to table: "verification_files"
DROP INDEX "verificationfile_verification_id";
-- reverse: create "verification_files" table
DROP TABLE "verification_files";
-- reverse: create index "verification_user_id_kind_status" to table: "verifications"
DROP INDEX "verification_user_id_kind_status";
-- reverse: create index "verification_status_created_at" to table: "verifications"
DROP INDEX "verification_status_created_at";
-- reverse: create index "verification_one_pending" to table: "verifications"
DROP INDEX "verification_one_pending";
-- reverse: create "verifications" table
DROP TABLE "verifications";
-- reverse: create index "landlord_profiles_user_id_key" to table: "landlord_profiles"
DROP INDEX "landlord_profiles_user_id_key";
-- reverse: create "landlord_profiles" table
DROP TABLE "landlord_profiles";
-- reverse: create index "agent_profiles_user_id_key" to table: "agent_profiles"
DROP INDEX "agent_profiles_user_id_key";
-- reverse: create "agent_profiles" table
DROP TABLE "agent_profiles";
-- reverse: modify "users" table
ALTER TABLE "users" DROP COLUMN "license_verified_at", DROP COLUMN "identity_verified_at", DROP COLUMN "notification_prefs", DROP COLUMN "data_saver", DROP COLUMN "avatar_key", DROP COLUMN "deleted_at", ALTER COLUMN "phone" SET NOT NULL;
