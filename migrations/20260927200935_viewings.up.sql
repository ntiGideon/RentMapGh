-- modify "users" table
ALTER TABLE "users" ADD COLUMN "viewing_hours" jsonb NULL;
-- create "saved_listings" table
CREATE TABLE "saved_listings" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "user_id" uuid NOT NULL, "listing_id" uuid NOT NULL, PRIMARY KEY ("id"));
-- create index "savedlisting_user_id_created_at" to table: "saved_listings"
CREATE INDEX "savedlisting_user_id_created_at" ON "saved_listings" ("user_id", "created_at");
-- create index "savedlisting_user_id_listing_id" to table: "saved_listings"
CREATE UNIQUE INDEX "savedlisting_user_id_listing_id" ON "saved_listings" ("user_id", "listing_id");
-- create "viewings" table
CREATE TABLE "viewings" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "listing_id" uuid NOT NULL, "renter_id" uuid NOT NULL, "lister_id" uuid NOT NULL, "status" character varying NOT NULL DEFAULT 'requested', "starts_at" timestamptz NOT NULL, "duration_min" bigint NOT NULL DEFAULT 30, "note" character varying NULL, "viewing_fee" bigint NULL, "fee_acknowledged" boolean NOT NULL DEFAULT false, "decline_reason" character varying NULL, "closed_by" uuid NULL, "confirmed_at" timestamptz NULL, "location_seen_at" timestamptz NULL, PRIMARY KEY ("id"));
-- create index "viewing_lister_id_starts_at" to table: "viewings"
CREATE INDEX "viewing_lister_id_starts_at" ON "viewings" ("lister_id", "starts_at");
-- create index "viewing_listing_id_status" to table: "viewings"
CREATE INDEX "viewing_listing_id_status" ON "viewings" ("listing_id", "status");
-- create index "viewing_renter_id_starts_at" to table: "viewings"
CREATE INDEX "viewing_renter_id_starts_at" ON "viewings" ("renter_id", "starts_at");
-- create index "viewing_status_starts_at" to table: "viewings"
CREATE INDEX "viewing_status_starts_at" ON "viewings" ("status", "starts_at");
