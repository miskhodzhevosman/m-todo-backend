package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type user struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}
type userKey struct{}

func currentUser(r *http.Request) user { u, _ := r.Context().Value(userKey{}).(user); return u }

const sessionCookie = "todo_session"

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{2,31}$`)

func (a *api) authenticate(next http.Handler, origins []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				allowed := err == nil && u.Host == r.Host && (u.Scheme == "http" || u.Scheme == "https")
				for _, configured := range origins {
					allowed = allowed || origin == configured
				}
				if !allowed {
					writeError(w, &apiError{403, "Origin not allowed"})
					return
				}
			}
		}
		if r.URL.Path == "/health" || r.URL.Path == "/auth/login" {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, &apiError{401, "Authentication required"})
			return
		}
		var u user
		err = a.db.QueryRowContext(r.Context(), `SELECT u.id,u.username FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires_at>?`, tokenHash(cookie.Value), time.Now().Unix()).Scan(&u.ID, &u.Username)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				err = &apiError{401, "Session expired. Please sign in"}
			}
			writeError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	})
}

func credentials(w http.ResponseWriter, r *http.Request) (string, string, error) {
	var payload struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := d.Decode(&payload); err != nil {
		return "", "", invalid("Expected username and password")
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", "", invalid("Expected a single JSON object")
	}
	return strings.TrimSpace(payload.Username), payload.Password, nil
}

func createUser(db *sql.DB, username, password string, claimLegacy bool) (user, error) {
	if !usernamePattern.MatchString(username) {
		return user{}, invalid("Username must be 3–32 letters, digits, _, . or -")
	}
	if len(password) < 8 || len(password) > 72 {
		return user{}, invalid("Password must be 8–72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return user{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return user{}, err
	}
	defer tx.Rollback()
	result, err := tx.Exec("INSERT INTO users (username,password_hash) VALUES (?,?)", username, string(hash))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return user{}, &apiError{409, "Username already exists"}
		}
		return user{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return user{}, err
	}
	if claimLegacy {
		for _, table := range []string{"categories", "todos"} {
			if _, err = tx.Exec("UPDATE "+table+" SET user_id=? WHERE user_id IS NULL", id); err != nil {
				return user{}, err
			}
		}
	}
	return user{ID: id, Username: username}, tx.Commit()
}

func (a *api) login(w http.ResponseWriter, r *http.Request) {
	name, password, err := credentials(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	var u user
	var hash string
	err = a.db.QueryRowContext(r.Context(), "SELECT id,username,password_hash FROM users WHERE username=?", name).Scan(&u.ID, &u.Username, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, err)
		return
	}
	if err != nil || len(password) > 72 || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		writeError(w, &apiError{401, "Invalid username or password"})
		return
	}
	a.startSession(w, r, u, http.StatusOK)
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func (a *api) startSession(w http.ResponseWriter, r *http.Request, u user, status int) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		writeError(w, err)
		return
	}
	token := hex.EncodeToString(random[:])
	expires := time.Now().Add(30 * 24 * time.Hour)
	_, err := a.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE expires_at <= ?", time.Now().Unix())
	if err != nil {
		writeError(w, err)
		return
	}
	_, err = a.db.ExecContext(r.Context(), "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)", tokenHash(token), u.ID, expires.Unix())
	if err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: a.secureCookies, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: 30 * 24 * 60 * 60})
	writeJSON(w, status, u)
}
func (a *api) me(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, currentUser(r)) }
func (a *api) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(sessionCookie)
	if _, err := a.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE token_hash=?", tokenHash(cookie.Value)); err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: a.secureCookies, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(204)
}
