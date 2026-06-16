## 2026-06-16 - Prevent SQL injection via PRAGMA statements
**Vulnerability:** A SQL injection vulnerability exists in `internal/storage/sqlite.go` because `sqliteColumnExists` concatenates unsanitized user input into a PRAGMA statement.
**Learning:** String formatting in SQLite queries, even for schema objects where standard parameterization isn't supported, is dangerous and can lead to SQL injection. Using SQLite's table-valued functions provides a safe alternative.
**Prevention:** Replace all dynamic PRAGMA statement formatting with parameterized queries against table-valued PRAGMA functions (e.g., `SELECT * FROM pragma_table_info(?)`).
