## 2024-06-04 - Fix SQL Injection in SQLite PRAGMA execution
**Vulnerability:** Unsafe string interpolation in `fmt.Sprintf("PRAGMA table_info(%s);", table)` allowed potential SQL injection if table names were controlled by an attacker or contained special characters.
**Learning:** `PRAGMA` statements in SQLite cannot use standard parameterized queries (e.g., `PRAGMA table_info(?)`), making developers prone to using unsafe `fmt.Sprintf`. However, SQLite 3.16.0+ supports the `pragma_table_info(?)` table-valued function which *can* be parameterized.
**Prevention:** Always use the table-valued function equivalents of `PRAGMA` statements (e.g., `SELECT ... FROM pragma_table_info(?)`) when dynamic arguments are needed to ensure safe parameterized execution.
