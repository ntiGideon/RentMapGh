-- reverse: create index "listingmedia_status" to table: "listing_media"
DROP INDEX "listingmedia_status";
-- reverse: modify "listing_media" table
ALTER TABLE "listing_media" DROP COLUMN "duration_ms", DROP COLUMN "status";
