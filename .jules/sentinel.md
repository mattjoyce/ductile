## 2025-06-09 - [Fix Parameterized PRAGMA Query in SQLite Storage]
**Vulnerability:** Use of `fmt.Sprintf("PRAGMA table_info(%s);", table)` which could allow SQL injection if the table name is ever sourced from user input.
**Learning:** `PRAGMA table_info` isn't safely parameterized using placeholders in some versions of SQLite or standard drivers, leading to string interpolation as a workaround. However, SQLite offers `pragma_table_info(?)` as a table-valued function which securely accepts parameters.
**Prevention:** Avoid string interpolation (`fmt.Sprintf`) in SQL queries. Always use table-valued functions (like `pragma_table_info(?)`) or securely validate inputs if parameterization is not possible.
