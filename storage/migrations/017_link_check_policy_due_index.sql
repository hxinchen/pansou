DROP INDEX IF EXISTS resources_check_due_idx;

CREATE INDEX resources_check_due_idx
    ON resources (check_status, last_checked_at, id)
    WHERE check_status IN (
        'pending',
        'valid',
        'unknown',
        'invalid',
        'expired',
        'cancelled',
        'violation',
        'locked'
    );
