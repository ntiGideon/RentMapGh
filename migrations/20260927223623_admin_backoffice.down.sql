-- reverse: create index "duplicatecandidate_status_score" to table: "duplicate_candidates"
DROP INDEX "duplicatecandidate_status_score";
-- reverse: create index "duplicatecandidate_listing_a_listing_b" to table: "duplicate_candidates"
DROP INDEX "duplicatecandidate_listing_a_listing_b";
-- reverse: create "duplicate_candidates" table
DROP TABLE "duplicate_candidates";
-- reverse: modify "users" table
ALTER TABLE "users" DROP COLUMN "suspension_note", DROP COLUMN "suspended_at";
-- reverse: modify "sessions" table
ALTER TABLE "sessions" DROP COLUMN "impersonator_id";
-- reverse: modify "messages" table
ALTER TABLE "messages" DROP COLUMN "flags_reviewed_at";
