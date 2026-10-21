-- Discarding these selections resets routing intent. Only roll back as part of
-- a coordinated rollback which also disables this device-selection protocol.
DROP TABLE IF EXISTS conversation_device_selections;
