package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	db, err := openDatabase(filepath.Join(t.TempDir(), "todos.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return newHandler(db, []string{"http://localhost:5173"})
}

func request(t *testing.T, handler http.Handler, method, path, body string, status int, target any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != status {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, response.Code, status, response.Body.String())
	}
	if target != nil {
		if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}
	return response
}

func TestCRUDAndCategoryDeletion(t *testing.T) {
	h := testHandler(t)
	var categories []category
	request(t, h, "GET", "/categories", "", 200, &categories)
	if categories == nil || len(categories) != 0 {
		t.Fatal("empty categories should be []")
	}
	var c category
	request(t, h, "POST", "/categories", `{"name":" Работа "}`, 201, &c)
	if c.Name != "Работа" || c.Color != "#6366f1" || c.CreatedAt == "" {
		t.Fatalf("unexpected category: %+v", c)
	}
	request(t, h, "POST", "/categories", `{"name":"Работа"}`, 409, nil)
	var task todo
	request(t, h, "POST", "/todos", fmt.Sprintf(`{"title":" Задача ","description":"text","category_id":%d}`, c.ID), 201, &task)
	if task.Title != "Задача" || task.Urgency != "medium" || task.Completed || task.Category == nil || task.Category.ID != c.ID {
		t.Fatalf("unexpected todo: %+v", task)
	}
	path := fmt.Sprintf("/todos/%d", task.ID)
	request(t, h, "PATCH", path, `{"description":null,"due_at":"2026-12-01T18:00:00+03:00","completed":true}`, 200, &task)
	if task.Description != nil || !task.Completed || task.DueAt == nil || task.Category == nil {
		t.Fatalf("PATCH lost omitted fields or failed to clear description: %+v", task)
	}
	request(t, h, "PATCH", fmt.Sprintf("/categories/%d", c.ID), `{"name":"Дом","color":"#abcdef"}`, 200, &c)
	request(t, h, "GET", path, "", 200, &task)
	if task.Category == nil || task.Category.Name != "Дом" {
		t.Fatal("nested category was not updated")
	}
	response := request(t, h, "DELETE", fmt.Sprintf("/categories/%d", c.ID), "", 204, nil)
	if response.Body.Len() != 0 {
		t.Fatal("204 response must have no body")
	}
	request(t, h, "GET", path, "", 200, &task)
	if task.CategoryID != nil || task.Category != nil {
		t.Fatal("deleting a category should clear the todo relation")
	}
	request(t, h, "PATCH", path, `{"due_at":null,"category_id":null}`, 200, &task)
	if task.DueAt != nil {
		t.Fatal("due_at was not cleared")
	}
	request(t, h, "DELETE", path, "", 204, nil)
	request(t, h, "GET", path, "", 404, nil)
	request(t, h, "DELETE", path, "", 404, nil)
}

func TestFiltersAndSorting(t *testing.T) {
	h := testHandler(t)
	var c category
	request(t, h, "POST", "/categories", `{"name":"Work"}`, 201, &c)
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	for _, payload := range []string{
		`{"title":"low","urgency":"low"}`,
		`{"title":"critical","urgency":"critical"}`,
		fmt.Sprintf(`{"title":"overdue","due_at":%q,"category_id":%d}`, past, c.ID),
	} {
		request(t, h, "POST", "/todos", payload, 201, nil)
	}
	var tasks []todo
	request(t, h, "GET", "/todos", "", 200, &tasks)
	if len(tasks) != 3 || tasks[0].Title != "overdue" || tasks[1].Title != "critical" {
		t.Fatalf("unexpected ordering: %+v", tasks)
	}
	request(t, h, "GET", fmt.Sprintf("/todos?overdue=true&completed=false&category_id=%d&urgency=medium", c.ID), "", 200, &tasks)
	if len(tasks) != 1 || tasks[0].Title != "overdue" {
		t.Fatalf("unexpected filtered tasks: %+v", tasks)
	}
	request(t, h, "PATCH", fmt.Sprintf("/todos/%d", tasks[0].ID), `{"completed":true}`, 200, nil)
	request(t, h, "GET", "/todos?overdue=true", "", 200, &tasks)
	if len(tasks) != 0 {
		t.Fatal("completed todo should not be overdue")
	}
}

func TestValidationAndRollback(t *testing.T) {
	h := testHandler(t)
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/todos", `{}`, 422},
		{"POST", "/todos", `{"title":" "}`, 422},
		{"POST", "/todos", `{"title":"x","urgency":"invalid"}`, 422},
		{"POST", "/todos", `{"title":"x","due_at":"invalid"}`, 422},
		{"POST", "/todos", `{"title":"x","category_id":999}`, 400},
		{"POST", "/todos", `{"title":"x"} {}`, 422},
		{"POST", "/categories", `{"name":"x","color":"red"}`, 422},
		{"PATCH", "/todos/999", `{}`, 400},
		{"PATCH", "/todos/999", `{"title":"x"}`, 404},
		{"PATCH", "/todos/999", `{"completed":null}`, 422},
		{"GET", "/todos/abc", "", 422},
		{"GET", "/todos?completed=invalid", "", 422},
		{"GET", "/todos?category_id=abc", "", 422},
		{"GET", "/todos?urgency=invalid", "", 422},
	} {
		request(t, h, test.method, test.path, test.body, test.status, nil)
	}
	var first, second category
	request(t, h, "POST", "/categories", `{"name":"First"}`, 201, &first)
	request(t, h, "POST", "/categories", `{"name":"Second"}`, 201, &second)
	request(t, h, "PATCH", fmt.Sprintf("/categories/%d", second.ID), `{"name":"First","color":"#ffffff"}`, 409, nil)
	var categories []category
	request(t, h, "GET", "/categories", "", 200, &categories)
	if len(categories) != 2 || categories[1].Name != "Second" || categories[1].Color != "#6366f1" {
		t.Fatalf("failed mutation was not rolled back: %+v", categories)
	}
}

func TestCORS(t *testing.T) {
	h := testHandler(t)
	for _, test := range []struct {
		origin string
		status int
	}{
		{"http://localhost:5173", 200},
		{"http://example.com", 400},
	} {
		req := httptest.NewRequest("OPTIONS", "/todos", nil)
		req.Header.Set("Origin", test.origin)
		req.Header.Set("Access-Control-Request-Method", "PATCH")
		req.Header.Set("Access-Control-Request-Headers", "content-type")
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != test.status {
			t.Fatalf("CORS status: %d", response.Code)
		}
		if test.status == 200 && (response.Header().Get("Access-Control-Allow-Origin") != test.origin || response.Header().Get("Access-Control-Allow-Credentials") != "true") {
			t.Fatal("missing CORS headers")
		}
	}
}

func TestLegacyDatabaseMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE todos (
		id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, description TEXT,
		completed INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL DEFAULT (datetime('now'))
	); INSERT INTO todos (title) VALUES ('existing');`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		db, err = openDatabase(path)
		if err != nil {
			t.Fatal(err)
		}
		var task todo
		h := newHandler(db, nil)
		request(t, h, "GET", "/todos/1", "", 200, &task)
		db.Close()
		if task.Title != "existing" || task.Urgency != "medium" || task.CategoryID != nil {
			t.Fatalf("migration lost data or defaults: %+v", task)
		}
	}
}

func TestConfigEnvironmentOverrides(t *testing.T) {
	// Change directory so this verifies loading a dedicated .env, not the local one.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".env"), []byte("HOST=127.0.0.2\nPORT=9000\nDB_PATH=local.db\nCORS_ORIGINS=http://one.test, http://two.test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOST", "PORT", "DB_PATH", "CORS_ORIGINS"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	t.Setenv("PORT", "9100")
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(dir) })
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "9100" || cfg.Host != "127.0.0.2" || cfg.DBPath != "local.db" || len(cfg.CORSOrigins) != 2 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}
