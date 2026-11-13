-- Migration 6: protected secrets (docs/M6-SPEC.md §2).
--
-- Encoding rules (owned by sqlite_secrets.go):
--   * secrets: name is the daemon's opaque secret name ("proxy", "site_login:<id>");
--     blob is ciphertext produced by the operating system for the current user
--     (Windows DPAPI). The store never sees a password in plain text.

CREATE TABLE secrets (
    name TEXT PRIMARY KEY,
    blob BLOB NOT NULL
) STRICT;
