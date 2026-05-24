## 2026-05-24 - [SQL Injection via String Formatting in PRAGMA table_info]
**Vulnerability:** Found a SQL injection risk in `internal/storage/sqlite.go` where `fmt.Sprintf("PRAGMA table_info(%s);", table)` was used to dynamically construct a schema inspection query.
**Learning:** SQLite `PRAGMA` statements historically do not support parameterization. This led to using string concatenation or `fmt.Sprintf` to build the query dynamically.
**Prevention:** Use SQLite's parameterizable table-valued functions instead. For `PRAGMA table_info`, the equivalent parameterized query is `SELECT cid, name, type, "notnull", dflt_value, pk FROM pragma_table_info(?)`. Ensure to double quote `"notnull"` as it's a reserved keyword.
