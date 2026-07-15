DO $$
DECLARE
    constraint_name TEXT;
BEGIN
    FOR constraint_name IN
        SELECT conname
        FROM pg_constraint
        WHERE conrelid = 'resources'::regclass
          AND contype = 'c'
          AND pg_get_constraintdef(oid) LIKE '%check_status%'
    LOOP
        EXECUTE format('ALTER TABLE resources DROP CONSTRAINT %I', constraint_name);
    END LOOP;
END $$;

ALTER TABLE resources
    ADD CONSTRAINT resources_check_status_check
    CHECK (check_status IN (
        'pending',
        'valid',
        'invalid',
        'expired',
        'cancelled',
        'violation',
        'locked',
        'unknown',
        'unsupported'
    ));

DROP INDEX IF EXISTS resources_check_due_idx;

CREATE INDEX IF NOT EXISTS resources_check_due_idx ON resources (check_status, last_checked_at)
    WHERE check_status IN ('pending', 'invalid', 'expired', 'cancelled', 'violation', 'locked', 'unknown');
