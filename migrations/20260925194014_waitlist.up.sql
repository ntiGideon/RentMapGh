-- create "waitlist_entries" table
CREATE TABLE "waitlist_entries" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "phone" character varying NOT NULL, "name" character varying NULL, "role" character varying NOT NULL DEFAULT 'renter', "area" character varying NULL, "source" character varying NULL, "consent" boolean NOT NULL DEFAULT false, PRIMARY KEY ("id"));
-- create index "waitlistentry_phone" to table: "waitlist_entries"
CREATE UNIQUE INDEX "waitlistentry_phone" ON "waitlist_entries" ("phone");
-- create index "waitlistentry_role_area" to table: "waitlist_entries"
CREATE INDEX "waitlistentry_role_area" ON "waitlist_entries" ("role", "area");
