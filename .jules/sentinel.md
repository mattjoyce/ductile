## 2024-05-18 - Parameterized Table-Valued Functions

**Vulnerability:** String interpolation used to construct SQLite `PRAGMA table_info(%s)` queries (`fmt.Sprintf("PRAGMA table_info(%s);", table)`). This exposes the application to SQL injection if user input controls the table name.
**Learning:** SQLite's `PRAGMA` statements do not support parameterization. This leads developers to mistakenly use string interpolation or concatenation.
**Prevention:** Use SQLite table-valued functions (e.g., `pragma_table_info(?)`) instead of `PRAGMA` statements. Table-valued functions fully support bound parameters.
