-- Run locally once, while Web/API and Worker are stopped, only when 000001
-- was previously applied by web_api. Execute as a PostgreSQL administrator,
-- not as web_api, algorithm_worker, or the application migration service.
--
-- This is an ownership bootstrap, not an application migration. It aligns an
-- existing 000001 database with the clean-deployment ownership model before
-- platform_migrator applies 000002 in its own transaction.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_roles
         WHERE rolname = 'platform_migrator' AND rolcanlogin
    ) THEN
        RAISE EXCEPTION 'platform_migrator LOGIN role must exist';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_roles
         WHERE rolname = 'platform_worker_repository_owner' AND NOT rolcanlogin
    ) THEN
        RAISE EXCEPTION 'platform_worker_repository_owner NOLOGIN role must exist';
    END IF;
END;
$$;

REASSIGN OWNED BY web_api TO platform_migrator;
ALTER SCHEMA public OWNER TO platform_migrator;

-- This role membership permits the isolated migration service to transfer
-- final Worker function ownership. INHERIT remains disabled for that service.
GRANT platform_worker_repository_owner TO platform_migrator WITH INHERIT FALSE, SET TRUE;
REVOKE platform_worker_repository_owner FROM web_api;
REVOKE platform_worker_repository_owner FROM algorithm_worker;
