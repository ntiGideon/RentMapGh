-- reverse: create index "notification_user_id_read_at" to table: "notifications"
DROP INDEX "notification_user_id_read_at";
-- reverse: create index "notification_user_id_created_at" to table: "notifications"
DROP INDEX "notification_user_id_created_at";
-- reverse: create "notifications" table
DROP TABLE "notifications";
-- reverse: modify "viewings" table
ALTER TABLE "viewings" DROP COLUMN "feedback_at", DROP COLUMN "feedback_note", DROP COLUMN "interested", DROP COLUMN "accuracy", DROP COLUMN "renter_outcome", DROP COLUMN "feedback_asked_at", DROP COLUMN "reminded_2_at", DROP COLUMN "reminded_24_at", DROP COLUMN "responded_at";
