## 2026-06-15 - Prevent SQL Injection via Parameterized PRAGMA Alternative
**Vulnerability:** SQL Injection in `sqliteColumnExists` where `fmt.Sprintf("PRAGMA table_info(%s);", table)` was used without input validation.
**Learning:** String interpolation in PRAGMA statements is unsafe. While PRAGMAs do not directly accept query parameters in SQLite, modern SQLite supports table-valued functions like `pragma_table_info(?)` that allow for parameterization.
**Prevention:** Replace string interpolation of PRAGMAs with parameterized table-valued functions where applicable, or strictly validate/allowlist identifiers if PRAGMA parameterization isn't supported.
