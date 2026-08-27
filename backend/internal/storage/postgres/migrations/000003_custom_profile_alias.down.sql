DROP TRIGGER IF EXISTS parameter_profiles_immutable ON parameter_profiles;
DROP FUNCTION IF EXISTS reject_immutable_parameter_profile_update();
CREATE TRIGGER parameter_profiles_immutable
    BEFORE UPDATE ON parameter_profiles
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_update();
