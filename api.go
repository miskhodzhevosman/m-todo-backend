package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"modernc.org/sqlite"
)

type api struct {
	db            *sql.DB
	secureCookies bool
}

func newHandler(db *sql.DB, origins []string, secure ...bool) http.Handler {
	a := &api{db: db}
	if len(secure) > 0 {
		a.secureCookies = secure[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /auth/login", a.login)
	mux.HandleFunc("POST /auth/logout", a.logout)
	mux.HandleFunc("GET /auth/me", a.me)
	mux.HandleFunc("GET /categories", a.listCategories)
	mux.HandleFunc("POST /categories", a.createCategory)
	mux.HandleFunc("PATCH /categories/{id}", a.updateCategory)
	mux.HandleFunc("DELETE /categories/{id}", a.deleteCategory)
	mux.HandleFunc("GET /todos", a.listTodos)
	mux.HandleFunc("POST /todos", a.createTodo)
	mux.HandleFunc("GET /todos/{id}", a.getTodo)
	mux.HandleFunc("PATCH /todos/{id}", a.updateTodo)
	mux.HandleFunc("DELETE /todos/{id}", a.deleteTodo)
	return cors(a.authenticate(mux, origins), origins)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, err error) {
	status, detail := http.StatusInternalServerError, "Internal server error"
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		status, detail = apiErr.status, apiErr.detail
	} else {
		log.Printf("request failed: %v", err)
	}
	writeJSON(w, status, map[string]any{"detail": detail})
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, invalid("id must be an integer")
	}
	return id, nil
}

func (a *api) listCategories(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), "SELECT id, name, color, created_at FROM categories WHERE user_id = ? ORDER BY name COLLATE NOCASE", currentUser(r).ID)
	if err != nil {
		writeError(w, err)
		return
	}
	defer rows.Close()
	items := []category{}
	for rows.Next() {
		item, err := scanCategory(rows)
		if err != nil {
			writeError(w, err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *api) createCategory(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, "categories", true)
}

func (a *api) updateCategory(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, "categories", false)
}

func (a *api) createTodo(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, "todos", true)
}

func (a *api) updateTodo(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, "todos", false)
}

// Both entities share transactional create/update behavior. Table and field names
// only come from the handlers and validation allowlists, never from request SQL.
func (a *api) mutate(w http.ResponseWriter, r *http.Request, entity string, create bool) {
	id := int64(0)
	var err error
	if !create {
		id, err = pathID(r)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	data, err := readPayload(w, r, entity, create)
	if err != nil {
		writeError(w, err)
		return
	}
	if create {
		data["user_id"] = currentUser(r).ID
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback()
	if categoryID, ok := data["category_id"]; ok && categoryID != nil {
		var exists int
		err := tx.QueryRowContext(r.Context(), "SELECT 1 FROM categories WHERE id = ? AND user_id = ?", categoryID, currentUser(r).ID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			err = &apiError{http.StatusBadRequest, "Category not found"}
		}
		if err != nil {
			writeError(w, err)
			return
		}
	}
	fields := make([]string, 0, len(data))
	for field := range data {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	values := make([]any, 0, len(fields)+1)
	clauses := make([]string, 0, len(fields))
	for _, field := range fields {
		values = append(values, data[field])
		if create {
			clauses = append(clauses, "?")
		} else {
			clauses = append(clauses, field+" = ?")
		}
	}
	query := "INSERT INTO " + entity + " (" + strings.Join(fields, ", ") + ") VALUES (" + strings.Join(clauses, ", ") + ")"
	if !create {
		query = "UPDATE " + entity + " SET " + strings.Join(clauses, ", ") + " WHERE id = ? AND user_id = ?"
		values = append(values, id, currentUser(r).ID)
	}
	result, err := tx.ExecContext(r.Context(), query, values...)
	if err != nil {
		var sqliteErr *sqlite.Error
		if entity == "categories" && errors.As(err, &sqliteErr) && sqliteErr.Code() == 2067 {
			err = &apiError{http.StatusConflict, "Category already exists"}
		}
		writeError(w, err)
		return
	}
	if create {
		id, err = result.LastInsertId()
	} else {
		var count int64
		count, err = result.RowsAffected()
		if err == nil && count == 0 {
			err = notFound(entity)
		}
	}
	if err != nil {
		writeError(w, err)
		return
	}
	var item any
	if entity == "todos" {
		item, err = scanTodo(tx.QueryRowContext(r.Context(), todoSelect+" WHERE t.id = ?", id))
	} else {
		item, err = scanCategory(tx.QueryRowContext(r.Context(), "SELECT id, name, color, created_at FROM categories WHERE id = ?", id))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	writeJSON(w, status, item)
}

func notFound(entity string) error {
	if entity == "categories" {
		return &apiError{http.StatusNotFound, "Category not found"}
	}
	return &apiError{http.StatusNotFound, "Todo not found"}
}

func (a *api) deleteCategory(w http.ResponseWriter, r *http.Request) {
	a.delete(w, r, "categories")
}

func (a *api) deleteTodo(w http.ResponseWriter, r *http.Request) {
	a.delete(w, r, "todos")
}

func (a *api) delete(w http.ResponseWriter, r *http.Request, entity string) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := a.db.ExecContext(r.Context(), "DELETE FROM "+entity+" WHERE id = ? AND user_id = ?", id, currentUser(r).ID)
	if err == nil {
		var count int64
		count, err = result.RowsAffected()
		if err == nil && count == 0 {
			err = notFound(entity)
		}
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) getTodo(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	item, err := scanTodo(a.db.QueryRowContext(r.Context(), todoSelect+" WHERE t.id = ? AND t.user_id = ?", id, currentUser(r).ID))
	if errors.Is(err, sql.ErrNoRows) {
		err = notFound("todos")
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *api) listTodos(w http.ResponseWriter, r *http.Request) {
	where, values := []string{"t.user_id = ?"}, []any{currentUser(r).ID}
	params := r.URL.Query()
	for _, field := range []string{"completed", "urgency", "category_id", "overdue"} {
		if !params.Has(field) {
			continue
		}
		value := params.Get(field)
		switch field {
		case "completed", "overdue":
			parsed, err := parseBool(value)
			if err != nil {
				writeError(w, invalid(field+" must be a boolean"))
				return
			}
			if field == "completed" {
				where = append(where, "t.completed = ?")
				values = append(values, parsed)
			} else if parsed {
				where = append(where, "t.due_at IS NOT NULL AND julianday(t.due_at) < julianday('now') AND t.completed = 0")
			}
		case "urgency":
			if !validUrgency(value) {
				writeError(w, invalid("urgency must be low, medium, high or critical"))
				return
			}
			where = append(where, "t.urgency = ?")
			values = append(values, value)
		case "category_id":
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				writeError(w, invalid("category_id must be an integer"))
				return
			}
			where = append(where, "t.category_id = ?")
			values = append(values, id)
		}
	}
	query := todoSelect
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += ` ORDER BY CASE WHEN t.due_at IS NULL THEN 1 ELSE 0 END, t.due_at ASC,
		CASE t.urgency WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END,
		t.created_at DESC`
	rows, err := a.db.QueryContext(r.Context(), query, values...)
	if err != nil {
		writeError(w, err)
		return
	}
	defer rows.Close()
	items := []todo{}
	for rows.Next() {
		item, err := scanTodo(rows)
		if err != nil {
			writeError(w, err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func parseBool(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "true", "1", "on", "yes", "t", "y":
		return true, nil
	case "false", "0", "off", "no", "f", "n":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean: %q", value)
}
