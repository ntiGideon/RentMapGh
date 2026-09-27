-- modify "listing_media" table
ALTER TABLE "listing_media" ADD COLUMN "status" character varying NOT NULL DEFAULT 'ready', ADD COLUMN "duration_ms" bigint NULL;
-- create index "listingmedia_status" to table: "listing_media"
CREATE INDEX "listingmedia_status" ON "listing_media" ("status");
