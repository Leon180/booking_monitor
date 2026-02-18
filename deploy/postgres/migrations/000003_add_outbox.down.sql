DROP TABLE IF NOT EXISTS events_outbox;
ALTER TABLE events DROP CONSTRAINT IF EXISTS check_available_tickets_non_negative;
