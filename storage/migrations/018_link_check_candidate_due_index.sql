CREATE INDEX IF NOT EXISTS resources_candidate_check_due_idx
    ON resources (check_status, candidate_checked_at, id)
    WHERE candidate_check_status IS NOT NULL;
