-- Hand-written: PostGIS and JSONB indexes Ent can't express.
-- Public search reads ONLY approx_geog (ProjectRequirement §6.1); there is
-- deliberately no geography column for the exact point.
ALTER TABLE "properties" ADD COLUMN "approx_geog" geography(Point, 4326)
  GENERATED ALWAYS AS (
    CASE WHEN "approx_lat" IS NULL OR "approx_lng" IS NULL THEN NULL
         ELSE ST_SetSRID(ST_MakePoint("approx_lng", "approx_lat"), 4326)::geography END
  ) STORED;
CREATE INDEX "property_approx_geog" ON "properties" USING GIST ("approx_geog");

-- "Has wifi" style filters: units.amenities @> '["wifi"]'
CREATE INDEX "unit_amenities" ON "units" USING GIN ("amenities" jsonb_path_ops);

-- Price filters on live listings.
CREATE INDEX "listingterms_monthly_equivalent" ON "listing_terms" ("monthly_equivalent");
