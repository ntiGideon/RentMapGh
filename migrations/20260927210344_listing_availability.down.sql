-- reverse: modify "listings" table
ALTER TABLE "listings" DROP COLUMN "rented_via", DROP COLUMN "rented_at", DROP COLUMN "stale_reported_at", DROP COLUMN "nudged_at";
