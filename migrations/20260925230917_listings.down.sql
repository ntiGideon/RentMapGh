-- reverse: create index "listing_terms_listing_id_key" to table: "listing_terms"
DROP INDEX "listing_terms_listing_id_key";
-- reverse: create "listing_terms" table
DROP TABLE "listing_terms";
-- reverse: create index "listing_unit_id" to table: "listings"
DROP INDEX "listing_unit_id";
-- reverse: create index "listing_status_submitted_at" to table: "listings"
DROP INDEX "listing_status_submitted_at";
-- reverse: create index "listing_lister_id_status" to table: "listings"
DROP INDEX "listing_lister_id_status";
-- reverse: create "listings" table
DROP TABLE "listings";
-- reverse: create index "unit_property_id" to table: "units"
DROP INDEX "unit_property_id";
-- reverse: create "units" table
DROP TABLE "units";
-- reverse: create index "property_owner_id" to table: "properties"
DROP INDEX "property_owner_id";
-- reverse: create index "property_created_by" to table: "properties"
DROP INDEX "property_created_by";
-- reverse: create "properties" table
DROP TABLE "properties";
