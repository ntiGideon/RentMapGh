-- reverse: create index "agentmandate_property_id_status" to table: "agent_mandates"
DROP INDEX "agentmandate_property_id_status";
-- reverse: create index "agentmandate_agent_id_property_id" to table: "agent_mandates"
DROP INDEX "agentmandate_agent_id_property_id";
-- reverse: create index "agentmandate_agent_id_created_at" to table: "agent_mandates"
DROP INDEX "agentmandate_agent_id_created_at";
-- reverse: create index "agent_mandates_token_hash_key" to table: "agent_mandates"
DROP INDEX "agent_mandates_token_hash_key";
-- reverse: create "agent_mandates" table
DROP TABLE "agent_mandates";
