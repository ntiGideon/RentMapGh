-- modify "messages" table
ALTER TABLE "messages" ADD COLUMN "flags_reviewed_at" timestamptz NULL;
-- modify "sessions" table
ALTER TABLE "sessions" ADD COLUMN "impersonator_id" uuid NULL;
-- modify "users" table
ALTER TABLE "users" ADD COLUMN "suspended_at" timestamptz NULL, ADD COLUMN "suspension_note" character varying NULL;
-- create "duplicate_candidates" table
CREATE TABLE "duplicate_candidates" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "listing_a" uuid NOT NULL, "listing_b" uuid NOT NULL, "distance_m" bigint NOT NULL, "photo_bits" bigint NULL, "text_similarity" double precision NOT NULL DEFAULT 0, "score" bigint NOT NULL, "status" character varying NOT NULL DEFAULT 'open', "handled_by" uuid NULL, "handled_at" timestamptz NULL, PRIMARY KEY ("id"));
-- create index "duplicatecandidate_listing_a_listing_b" to table: "duplicate_candidates"
CREATE UNIQUE INDEX "duplicatecandidate_listing_a_listing_b" ON "duplicate_candidates" ("listing_a", "listing_b");
-- create index "duplicatecandidate_status_score" to table: "duplicate_candidates"
CREATE INDEX "duplicatecandidate_status_score" ON "duplicate_candidates" ("status", "score");
