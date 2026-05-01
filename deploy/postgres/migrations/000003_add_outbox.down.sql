-- CP4c: was `IF NOT EXISTS` (invalid syntax on DROP); corrected to `IF EXISTS`.
DROP TABLE IF EXISTS events_outbox;
ALTER TABLE events DROP CONSTRAINT IF EXISTS check_available_tickets_non_negative;
