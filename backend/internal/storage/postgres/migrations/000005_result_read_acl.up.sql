-- Web/API reads only committed artifact metadata and the Worker-produced
-- alarm index. Worker table access remains exclusively behind functions for
-- algorithm_worker; this grant is for the read-only Web/API role only.

GRANT SELECT ON TABLE artifacts, alarm_index TO web_api;
