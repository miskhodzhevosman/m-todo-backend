package main

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

func openDatabase(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// Keep connection-local PRAGMAs active and serialize SQLite writes.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;"); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	// Rebuild the old globally-unique categories table without rewriting todo FKs.
	if _, err := db.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer db.Exec("PRAGMA foreign_keys = ON")
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
		CREATE TABLE IF NOT EXISTS categories (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			color TEXT NOT NULL DEFAULT '#6366f1',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE IF NOT EXISTS todos (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			description TEXT,
			completed INTEGER NOT NULL DEFAULT 0,
			urgency TEXT NOT NULL DEFAULT 'medium'
				CHECK (urgency IN ('low', 'medium', 'high', 'critical')),
			due_at TEXT,
			category_id INTEGER REFERENCES categories(id) ON DELETE SET NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
	`)
	if err != nil {
		return err
	}
	rows, err := tx.Query("PRAGMA table_info(todos)")
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, column := range []struct{ name, ddl string }{
		{"urgency", "TEXT NOT NULL DEFAULT 'medium'"},
		{"due_at", "TEXT"},
		{"category_id", "INTEGER REFERENCES categories(id) ON DELETE SET NULL"},
	} {
		if !columns[column.name] {
			if _, err := tx.Exec("ALTER TABLE todos ADD COLUMN " + column.name + " " + column.ddl); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`
		CREATE INDEX IF NOT EXISTS idx_todos_category ON todos(category_id);
		CREATE INDEX IF NOT EXISTS idx_todos_due ON todos(due_at);
	`); err != nil {
		return err
	}
	if err := migrateUsers(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateUsers(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL COLLATE NOCASE UNIQUE,
		password_hash TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE TABLE IF NOT EXISTS sessions (
		token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at INTEGER NOT NULL
	);`); err != nil {
		return err
	}
	for _, table := range []string{"categories", "todos"} {
		rows, err := tx.Query("SELECT name FROM pragma_table_info(?)", table)
		if err != nil {
			return err
		}
		found := false
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			found = found || name == "user_id"
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if found {
			continue
		}
		if table == "categories" {
			_, err = tx.Exec(`CREATE TABLE categories_owned (
				id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, color TEXT NOT NULL DEFAULT '#6366f1',
				created_at TEXT NOT NULL DEFAULT (datetime('now')), user_id INTEGER REFERENCES users(id),
				UNIQUE(user_id, name)
			); INSERT INTO categories_owned (id,name,color,created_at) SELECT id,name,color,created_at FROM categories;
			DROP TABLE categories; ALTER TABLE categories_owned RENAME TO categories;`)
		} else {
			_, err = tx.Exec("ALTER TABLE todos ADD COLUMN user_id INTEGER REFERENCES users(id)")
		}
		if err != nil {
			return err
		}
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_todos_user ON todos(user_id);
		CREATE INDEX IF NOT EXISTS idx_categories_user ON categories(user_id);
		CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);`)
	return err
}

type category struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	CreatedAt string `json:"created_at"`
}

type todo struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	Description *string   `json:"description"`
	Completed   bool      `json:"completed"`
	Urgency     string    `json:"urgency"`
	DueAt       *string   `json:"due_at"`
	CreatedAt   string    `json:"created_at"`
	CategoryID  *int64    `json:"category_id"`
	Category    *category `json:"category"`
}

const todoSelect = `SELECT t.id, t.title, t.description, t.completed, t.urgency,
	t.due_at, t.created_at, t.category_id, c.name, c.color, c.created_at
	FROM todos t LEFT JOIN categories c ON c.id = t.category_id`

type scanner interface {
	Scan(...any) error
}

func scanCategory(row scanner) (category, error) {
	var c category
	err := row.Scan(&c.ID, &c.Name, &c.Color, &c.CreatedAt)
	return c, err
}

func scanTodo(row scanner) (todo, error) {
	var t todo
	var name, color, createdAt *string
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.Completed, &t.Urgency,
		&t.DueAt, &t.CreatedAt, &t.CategoryID, &name, &color, &createdAt)
	if err == nil && name != nil && t.CategoryID != nil {
		t.Category = &category{ID: *t.CategoryID, Name: *name, Color: *color, CreatedAt: *createdAt}
	}
	return t, err
}
