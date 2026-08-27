-- The projection is owned by the approved NOLOGIN Worker Repository role.
-- Temporarily permit schema CREATE only to switch to that owner for removal.
GRANT CREATE ON SCHEMA public TO platform_worker_repository_owner;
SET LOCAL ROLE platform_worker_repository_owner;
DROP FUNCTION IF EXISTS dataset_preflight_projection(TEXT);
RESET ROLE;
REVOKE CREATE ON SCHEMA public FROM platform_worker_repository_owner;

ALTER TABLE datasets
    DROP CONSTRAINT IF EXISTS datasets_original_filename_safe,
    DROP COLUMN IF EXISTS original_filename;
