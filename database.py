import sqlite3
from contextlib import contextmanager
from pathlib import Path

DB_PATH = Path(__file__).parent / "todos.db"


@contextmanager
def get_db():
    conn = sqlite3.connect(DB_PATH)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA foreign_keys = ON")
    try:
        yield conn
        conn.commit()
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()


def init_db():
    with get_db() as conn:
        conn.executescript("""
            CREATE TABLE IF NOT EXISTS categories (
                id         INTEGER PRIMARY KEY AUTOINCREMENT,
                name       TEXT NOT NULL UNIQUE,
                color      TEXT NOT NULL DEFAULT '#6366f1',
                created_at TEXT NOT NULL DEFAULT (datetime('now'))
            );

            CREATE TABLE IF NOT EXISTS todos (
                id          INTEGER PRIMARY KEY AUTOINCREMENT,
                title       TEXT NOT NULL,
                description TEXT,
                completed   INTEGER NOT NULL DEFAULT 0,
                urgency     TEXT NOT NULL DEFAULT 'medium'
                              CHECK (urgency IN ('low','medium','high','critical')),
                due_at      TEXT,
                category_id INTEGER REFERENCES categories(id) ON DELETE SET NULL,
                created_at  TEXT NOT NULL DEFAULT (datetime('now'))
            );
        """)


def create_indexes():
    """Вызывать ПОСЛЕ run_migrations(), иначе колонок может ещё не быть."""
    with get_db() as conn:
        conn.executescript("""
            CREATE INDEX IF NOT EXISTS idx_todos_category ON todos(category_id);
            CREATE INDEX IF NOT EXISTS idx_todos_due ON todos(due_at);
        """)
