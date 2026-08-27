-- CUSTOM aliases are mutable human-facing labels only. Canonical parameter
-- material remains immutable and REFERENCE is always read-only.

CREATE OR REPLACE FUNCTION reject_immutable_parameter_profile_update() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.mode = 'REFERENCE' OR
       NEW.version_id IS DISTINCT FROM OLD.version_id OR
       NEW.mode IS DISTINCT FROM OLD.mode OR
       NEW.base_version_id IS DISTINCT FROM OLD.base_version_id OR
       NEW.contract_version IS DISTINCT FROM OLD.contract_version OR
       NEW.shared_parameters IS DISTINCT FROM OLD.shared_parameters OR
       NEW.agents_json IS DISTINCT FROM OLD.agents_json OR
       NEW.fixed_items IS DISTINCT FROM OLD.fixed_items OR
       NEW.normalized_json IS DISTINCT FROM OLD.normalized_json OR
       NEW.normalized_sha256 IS DISTINCT FROM OLD.normalized_sha256 OR
       NEW.immutable IS DISTINCT FROM OLD.immutable OR
       NEW.created_at IS DISTINCT FROM OLD.created_at OR
       NEW.updated_at IS DISTINCT FROM OLD.updated_at THEN
        RAISE EXCEPTION 'immutable parameter profile content cannot be updated' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

REVOKE ALL ON FUNCTION reject_immutable_parameter_profile_update() FROM PUBLIC;

DROP TRIGGER IF EXISTS parameter_profiles_immutable ON parameter_profiles;
CREATE TRIGGER parameter_profiles_immutable
    BEFORE UPDATE ON parameter_profiles
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_parameter_profile_update();
