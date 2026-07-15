ALTER TABLE resources
    ADD CONSTRAINT resources_candidate_check_status_check
    CHECK (
        candidate_check_status IS NULL
        OR candidate_check_status IN ('invalid', 'expired', 'cancelled', 'violation')
    ) NOT VALID,
    ADD CONSTRAINT resources_candidate_check_pair_check
    CHECK (
        (candidate_check_status IS NULL) = (candidate_checked_at IS NULL)
    ) NOT VALID;
