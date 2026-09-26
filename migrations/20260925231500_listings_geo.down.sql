DROP INDEX IF EXISTS "listingterms_monthly_equivalent";
DROP INDEX IF EXISTS "unit_amenities";
DROP INDEX IF EXISTS "property_approx_geog";
ALTER TABLE "properties" DROP COLUMN IF EXISTS "approx_geog";
