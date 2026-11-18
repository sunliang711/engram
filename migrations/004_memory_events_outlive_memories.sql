-- memory_events is an append-only change log; its rows must remain after a memory is
-- permanently deleted so clients following the change feed still observe the deletion.
ALTER TABLE memory_events
    DROP CONSTRAINT IF EXISTS memory_events_memory_id_fkey;
