-- create "properties" table
CREATE TABLE "properties" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "created_by" uuid NOT NULL, "owner_id" uuid NULL, "name" character varying NULL, "category" character varying NOT NULL DEFAULT 'residential', "lat" double precision NULL, "lng" double precision NULL, "approx_lat" double precision NULL, "approx_lng" double precision NULL, "digital_address" character varying NULL, "landmark" character varying NULL, "street" character varying NULL, "neighbourhood" character varying NULL, "city" character varying NOT NULL DEFAULT 'Kumasi', "region" character varying NOT NULL DEFAULT 'Ashanti', PRIMARY KEY ("id"));
-- create index "property_created_by" to table: "properties"
CREATE INDEX "property_created_by" ON "properties" ("created_by");
-- create index "property_owner_id" to table: "properties"
CREATE INDEX "property_owner_id" ON "properties" ("owner_id");
-- create "units" table
CREATE TABLE "units" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "label" character varying NULL, "unit_type" character varying NULL, "bedrooms" bigint NULL, "bathrooms" bigint NULL, "size_sqm" bigint NULL, "floor" bigint NULL, "furnished" character varying NULL, "self_contained" boolean NULL, "meter_type" character varying NULL, "water_source" character varying NULL, "kitchen" character varying NULL, "amenities" jsonb NULL, "property_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "units_properties_units" FOREIGN KEY ("property_id") REFERENCES "properties" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- create index "unit_property_id" to table: "units"
CREATE INDEX "unit_property_id" ON "units" ("property_id");
-- create "listings" table
CREATE TABLE "listings" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "lister_id" uuid NOT NULL, "lister_kind" character varying NOT NULL, "status" character varying NOT NULL DEFAULT 'draft', "wizard_step" character varying NOT NULL DEFAULT 'location', "headline" character varying NULL, "description" character varying NULL, "available_from" timestamptz NULL, "last_confirmed_at" timestamptz NULL, "quality_score" bigint NOT NULL DEFAULT 0, "trust_score" bigint NOT NULL DEFAULT 0, "views_count" bigint NOT NULL DEFAULT 0, "promoted_until" timestamptz NULL, "submitted_at" timestamptz NULL, "published_at" timestamptz NULL, "reviewed_by" uuid NULL, "review_note" character varying NULL, "unit_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "listings_units_listings" FOREIGN KEY ("unit_id") REFERENCES "units" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- create index "listing_lister_id_status" to table: "listings"
CREATE INDEX "listing_lister_id_status" ON "listings" ("lister_id", "status");
-- create index "listing_status_submitted_at" to table: "listings"
CREATE INDEX "listing_status_submitted_at" ON "listings" ("status", "submitted_at");
-- create index "listing_unit_id" to table: "listings"
CREATE INDEX "listing_unit_id" ON "listings" ("unit_id");
-- create "listing_terms" table
CREATE TABLE "listing_terms" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "rent" bigint NULL, "rent_period" character varying NOT NULL DEFAULT 'month', "advance_periods" bigint NULL, "deposit" bigint NULL, "agent_fee" bigint NULL, "service_charge" bigint NULL, "viewing_fee" bigint NULL, "other_fees" jsonb NULL, "monthly_equivalent" bigint NULL, "move_in_total" bigint NULL, "negotiable" boolean NOT NULL DEFAULT false, "min_lease_months" bigint NULL, "listing_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "listing_terms_listings_terms" FOREIGN KEY ("listing_id") REFERENCES "listings" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- create index "listing_terms_listing_id_key" to table: "listing_terms"
CREATE UNIQUE INDEX "listing_terms_listing_id_key" ON "listing_terms" ("listing_id");
