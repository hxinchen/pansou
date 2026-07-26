ALTER TABLE collection_run_items
    DROP CONSTRAINT collection_run_items_status_check;

ALTER TABLE collection_run_items
    ADD CONSTRAINT collection_run_items_status_check
    CHECK (status IN ('pending', 'running', 'success', 'success_empty', 'partial', 'failed'));

ALTER TABLE collection_runs
    DROP CONSTRAINT collection_runs_status_check;

ALTER TABLE collection_runs
    ADD CONSTRAINT collection_runs_status_check
    CHECK (status IN ('pending', 'running', 'success', 'success_empty', 'partial', 'failed'));
