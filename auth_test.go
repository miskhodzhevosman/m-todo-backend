package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func sessionHandler(h http.Handler, cookie *http.Cookie) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { r.AddCookie(cookie); h.ServeHTTP(w, r) })
}

func TestAuthenticationAndIsolation(t *testing.T) {
	db, err := openDatabase(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newHandler(db, nil, true)
	request(t, h, "GET", "/todos", "", 401, nil)
	request(t, h, "GET", "/health", "", 200, nil)
	register := request(t, h, "POST", "/auth/register", `{"username":"alice","password":"password123"}`, 201, nil)
	cookie := register.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("session cookie is not protected")
	}
	alice := sessionHandler(h, cookie)
	var me user
	request(t, alice, "GET", "/auth/me", "", 200, &me)
	if me.Username != "alice" {
		t.Fatal(me)
	}
	request(t, h, "POST", "/auth/login", `{"username":"alice","password":"wrong"}`, 401, nil)
	request(t, h, "POST", "/auth/register", `{"username":"ALICE","password":"password123"}`, 409, nil)
	request(t, h, "POST", "/auth/register", `{"username":"short","password":"123"}`, 422, nil)
	var cat category
	var task todo
	request(t, alice, "POST", "/categories", `{"name":"Private"}`, 201, &cat)
	request(t, alice, "POST", "/todos", fmt.Sprintf(`{"title":"secret","category_id":%d}`, cat.ID), 201, &task)
	bobResponse := request(t, h, "POST", "/auth/register", `{"username":"bob","password":"password123"}`, 201, nil)
	bob := sessionHandler(h, bobResponse.Result().Cookies()[0])
	var todos []todo
	var categories []category
	request(t, bob, "GET", "/todos", "", 200, &todos)
	request(t, bob, "GET", "/categories", "", 200, &categories)
	if len(todos) != 0 || len(categories) != 0 {
		t.Fatal("another user's records exposed")
	}
	request(t, bob, "GET", fmt.Sprintf("/todos/%d", task.ID), "", 404, nil)
	request(t, bob, "PATCH", fmt.Sprintf("/todos/%d", task.ID), `{"title":"stolen"}`, 404, nil)
	request(t, bob, "DELETE", fmt.Sprintf("/todos/%d", task.ID), "", 404, nil)
	request(t, bob, "PATCH", fmt.Sprintf("/categories/%d", cat.ID), `{"name":"stolen"}`, 404, nil)
	request(t, bob, "DELETE", fmt.Sprintf("/categories/%d", cat.ID), "", 404, nil)
	request(t, bob, "POST", "/todos", fmt.Sprintf(`{"title":"bad relation","category_id":%d}`, cat.ID), 400, nil)
	request(t, bob, "POST", "/categories", `{"name":"Private"}`, 201, nil)
	request(t, alice, "GET", fmt.Sprintf("/todos/%d", task.ID), "", 200, &task)
	if task.Title != "secret" {
		t.Fatal("another user changed the task")
	}
	csrf := httptest.NewRequest("POST", "/todos", nil)
	csrf.Header.Set("Origin", "https://evil.example")
	csrf.AddCookie(cookie)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, csrf)
	if response.Code != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
	request(t, alice, "POST", "/auth/logout", "", 204, nil)
	request(t, alice, "GET", "/auth/me", "", 401, nil)
	login := request(t, h, "POST", "/auth/login", `{"username":"ALICE","password":"password123"}`, 200, nil)
	if _, err := db.Exec("UPDATE sessions SET expires_at=?", time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	request(t, sessionHandler(h, login.Result().Cookies()[0]), "GET", "/auth/me", "", 401, nil)
}

func TestLegacyOwnershipAndMigration(t *testing.T) {
	db, err := openDatabase(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec("INSERT INTO categories(name) VALUES('Legacy'); INSERT INTO todos(title,category_id) VALUES('Legacy task',1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = createUser(db, "random", "password123", false); err != nil {
		t.Fatal(err)
	}
	var owner *int64
	if err = db.QueryRow("SELECT user_id FROM todos").Scan(&owner); err != nil || owner != nil {
		t.Fatal("public registration claimed legacy data")
	}
	u, err := createUser(db, "owner", "password123", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT user_id FROM todos").Scan(&owner); err != nil || owner == nil || *owner != u.ID {
		t.Fatal("legacy data not assigned")
	}
	if err = migrate(db); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Fatal("invalid foreign keys after migration")
	}
	rows.Close()
	var enabled int
	db.QueryRow("PRAGMA foreign_keys").Scan(&enabled)
	if enabled != 1 {
		t.Fatal("foreign keys disabled")
	}
	_, err = db.Exec("DELETE FROM categories WHERE id=1")
	if err != nil {
		t.Fatal(err)
	}
	var categoryID *int64
	if err = db.QueryRow("SELECT category_id FROM todos").Scan(&categoryID); err != nil || categoryID != nil {
		t.Fatal("category delete does not clear relation")
	}
}
