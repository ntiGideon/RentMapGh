-- create "listing_media" table
CREATE TABLE "listing_media" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "uploaded_by" uuid NOT NULL, "kind" character varying NOT NULL DEFAULT 'photo', "position" bigint NOT NULL DEFAULT 0, "width" bigint NOT NULL, "height" bigint NOT NULL, "bytes" bigint NOT NULL, "blurhash" character varying NULL, "phash" bigint NOT NULL, "listing_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "listing_media_listings_media" FOREIGN KEY ("listing_id") REFERENCES "listings" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- create index "listingmedia_listing_id_position" to table: "listing_media"
CREATE INDEX "listingmedia_listing_id_position" ON "listing_media" ("listing_id", "position");
