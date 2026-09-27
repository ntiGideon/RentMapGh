-- reverse: create index "viewing_status_starts_at" to table: "viewings"
DROP INDEX "viewing_status_starts_at";
-- reverse: create index "viewing_renter_id_starts_at" to table: "viewings"
DROP INDEX "viewing_renter_id_starts_at";
-- reverse: create index "viewing_listing_id_status" to table: "viewings"
DROP INDEX "viewing_listing_id_status";
-- reverse: create index "viewing_lister_id_starts_at" to table: "viewings"
DROP INDEX "viewing_lister_id_starts_at";
-- reverse: create "viewings" table
DROP TABLE "viewings";
-- reverse: create index "savedlisting_user_id_listing_id" to table: "saved_listings"
DROP INDEX "savedlisting_user_id_listing_id";
-- reverse: create index "savedlisting_user_id_created_at" to table: "saved_listings"
DROP INDEX "savedlisting_user_id_created_at";
-- reverse: create "saved_listings" table
DROP TABLE "saved_listings";
-- reverse: modify "users" table
ALTER TABLE "users" DROP COLUMN "viewing_hours";
