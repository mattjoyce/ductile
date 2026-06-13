## 2026-06-13 - Fix SQL Injection in SQLite Schema Validation
**Vulnerability:** A SQL injection vulnerability existed in `internal/storage/sqlite.go` within the `sqliteColumnExists` function where the `table` variable was unsafely formatted directly into the `PRAGMA table_info(%s);` query string using `fmt.Sprintf`.
**Learning:** This occurred because developers often assume table names are internal and safe, and standard SQLite `PRAGMA` statements did not historically support parameterized queries, leading to the use of string interpolation as a workaround.
**Prevention:** Use SQLite's newer table-valued functions (e.g., `pragma_table_info(?)`) which support parameterized queries to safely pass user or variable input for schema metadata checks.
