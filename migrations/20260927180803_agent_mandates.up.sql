-- create "agent_mandates" table
CREATE TABLE "agent_mandates" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "agent_id" uuid NOT NULL, "property_id" uuid NOT NULL, "landlord_phone" character varying NOT NULL, "landlord_name" character varying NULL, "granted_by" uuid NULL, "status" character varying NOT NULL DEFAULT 'pending', "months" bigint NOT NULL DEFAULT 12, "token_hash" bytea NOT NULL, "sent_at" timestamptz NOT NULL, "sends" bigint NOT NULL DEFAULT 1, "decided_at" timestamptz NULL, "valid_until" timestamptz NULL, "reported" boolean NOT NULL DEFAULT false, PRIMARY KEY ("id"));
-- create index "agent_mandates_token_hash_key" to table: "agent_mandates"
CREATE UNIQUE INDEX "agent_mandates_token_hash_key" ON "agent_mandates" ("token_hash");
-- create index "agentmandate_agent_id_created_at" to table: "agent_mandates"
CREATE INDEX "agentmandate_agent_id_created_at" ON "agent_mandates" ("agent_id", "created_at");
-- create index "agentmandate_agent_id_property_id" to table: "agent_mandates"
CREATE INDEX "agentmandate_agent_id_property_id" ON "agent_mandates" ("agent_id", "property_id");
-- create index "agentmandate_property_id_status" to table: "agent_mandates"
CREATE INDEX "agentmandate_property_id_status" ON "agent_mandates" ("property_id", "status");
