## 2024-05-26 - [Fix SQL Injection Vulnerability in sqliteColumnExists]
**Vulnerability:** The `sqliteColumnExists` function in `internal/storage/sqlite.go` was using `fmt.Sprintf` to directly interpolate the `table` variable into the PRAGMA query string.
**Learning:** This approach bypasses SQLite's built-in parameterization mechanisms, opening the application to potential SQL injection if the table variable is ever sourced from user input or unvalidated external sources.
**Prevention:** Use SQLite's parameterized table-valued functions like `pragma_table_info(?)` which safely handles database identifiers.
