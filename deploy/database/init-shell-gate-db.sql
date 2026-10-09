-- Run as a PostgreSQL administrator using psql, not a database MCP.
-- Stop on any error; choose an unoccupied database/role name.
\set ON_ERROR_STOP on
CREATE ROLE shell_gate_app LOGIN;
\password shell_gate_app
CREATE DATABASE shell_gate OWNER shell_gate_app;
\connect shell_gate
GRANT USAGE, CREATE ON SCHEMA public TO shell_gate_app;
