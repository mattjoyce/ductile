## 2024-06-07 - Fix SQL Injection in sqliteColumnExists
**Vulnerability:** A SQL injection vulnerability existed in `internal/storage/sqlite.go` where `table` names were being directly interpolated into a `PRAGMA table_info(%s)` query string using `fmt.Sprintf`.
**Learning:** String formatting should never be used to dynamically construct SQL queries, even for schema verification with PRAGMAs, because it leaves the application vulnerable to injection if the input is unsanitized or manipulated.
**Prevention:** Use parameterized table-valued functions (e.g. `SELECT name FROM pragma_table_info(?) WHERE name = ? LIMIT 1;`) with `QueryRowContext` to safely pass dynamic schema parameters while allowing SQLite to securely escape inputs.
