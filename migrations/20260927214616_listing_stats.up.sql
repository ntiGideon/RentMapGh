-- create "listing_stats" table
CREATE TABLE "listing_stats" ("id" uuid NOT NULL, "listing_id" uuid NOT NULL, "day" timestamptz NOT NULL, "views" bigint NOT NULL DEFAULT 0, "saves" bigint NOT NULL DEFAULT 0, "contacts" bigint NOT NULL DEFAULT 0, PRIMARY KEY ("id"));
-- create index "listingstat_day" to table: "listing_stats"
CREATE INDEX "listingstat_day" ON "listing_stats" ("day");
-- create index "listingstat_listing_id_day" to table: "listing_stats"
CREATE UNIQUE INDEX "listingstat_listing_id_day" ON "listing_stats" ("listing_id", "day");
