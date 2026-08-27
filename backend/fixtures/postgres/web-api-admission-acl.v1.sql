-- Run as the real web_api role after migrations 000001..000004 in a disposable
-- PostgreSQL database. This fixture proves the narrow admission read surface:
-- no UPDATE permission is required on immutable parent tables.

BEGIN;

DO $$
BEGIN
    IF current_user <> 'web_api' THEN
        RAISE EXCEPTION 'fixture must run as web_api, got %', current_user;
    END IF;
    IF NOT has_table_privilege(current_user, 'public.datasets', 'SELECT') OR
       has_table_privilege(current_user, 'public.datasets', 'UPDATE') THEN
        RAISE EXCEPTION 'web_api datasets ACL is not SELECT-only';
    END IF;
    IF NOT has_table_privilege(current_user, 'public.parameter_profiles', 'SELECT') OR
       has_table_privilege(current_user, 'public.parameter_profiles', 'UPDATE') THEN
        RAISE EXCEPTION 'web_api parameter profile table ACL is broader than required';
    END IF;
    IF NOT has_column_privilege(current_user, 'public.parameter_profiles', 'display_name', 'UPDATE') OR
       has_column_privilege(current_user, 'public.parameter_profiles', 'normalized_json', 'UPDATE') THEN
        RAISE EXCEPTION 'web_api alias column ACL is not exact';
    END IF;
    IF NOT has_table_privilege(current_user, 'public.scheduler_control', 'UPDATE') OR
       NOT has_table_privilege(current_user, 'public.idempotency_keys', 'UPDATE') THEN
        RAISE EXCEPTION 'web_api admission serialization locks are not authorized';
    END IF;
END;
$$;

-- Plain reads are the only direct parent-table operations used by admission.
SELECT dataset_id FROM datasets WHERE FALSE;
SELECT version_id FROM parameter_profiles WHERE FALSE;
SELECT version_id FROM load_mapping_profiles WHERE FALSE;

ROLLBACK;
