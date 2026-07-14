-- Establish the durable cutover marker for the legacy filesystem mailbox.
-- The daemon imports bounded legacy mailbox data into issue_observation_events
-- and atomically advances this marker to complete with the imported rows.
INSERT INTO meta(key, value)
VALUES ('issue:mailbox_observation_projection_cutover', '{"state":"pending","version":1}')
ON CONFLICT(key) DO NOTHING;
