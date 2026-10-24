from database import get_db

TODO_COLUMNS = {
    "urgency": "TEXT NOT NULL DEFAULT 'medium'",
    "due_at": "TEXT",
    "category_id": "INTEGER REFERENCES categories(id) ON DELETE SET NULL",
}


def _columns(conn, table: str) -> set[str]:
    rows = conn.execute(f"PRAGMA table_info({table})").fetchall()
    return {r["name"] for r in rows}


def run_migrations():
    with get_db() as conn:
        # 1. categories создаётся в init_db() через IF NOT EXISTS
        # 2. добавляем недостающие колонки в todos
        existing = _columns(conn, "todos")
        for name, ddl in TODO_COLUMNS.items():
            if name not in existing:
                conn.execute(f"ALTER TABLE todos ADD COLUMN {name} {ddl}")
