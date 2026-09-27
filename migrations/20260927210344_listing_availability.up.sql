-- modify "listings" table
ALTER TABLE "listings" ADD COLUMN "nudged_at" timestamptz NULL, ADD COLUMN "stale_reported_at" timestamptz NULL, ADD COLUMN "rented_at" timestamptz NULL, ADD COLUMN "rented_via" character varying NULL;
