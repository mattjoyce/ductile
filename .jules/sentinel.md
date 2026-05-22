## 2026-05-22 - [SQL Injection via PRAGMA Statements]
**Vulnerability:** Found a potential SQL injection vulnerability in `sqliteColumnExists` where `fmt.Sprintf("PRAGMA table_info(%s);", table)` was used to dynamically construct a SQL query using string interpolation instead of parameterized queries.
**Learning:** PRAGMA statements cannot be parameterized natively, which leads developers to use string formatting, opening the door for SQL injection if the table name is derived from untrusted input.
**Prevention:** Use the table-valued function `pragma_table_info(?)` which allows parameterization instead of using `PRAGMA table_info` directly. Also make sure to quote the `"notnull"` column because it is a reserved keyword.
