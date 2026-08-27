-- Permit the Web/API to rename only the human-readable CUSTOM alias. The
-- parameter profile trigger continues to reject every canonical field change.

GRANT UPDATE (display_name) ON TABLE parameter_profiles TO web_api;
