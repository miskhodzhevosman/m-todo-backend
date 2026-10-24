package main

import (
	"net/http"
	"strings"
)

func cors(next http.Handler, origins []string) http.Handler {
	allowed := make(map[string]bool, len(origins))
	for _, origin := range origins {
		allowed[origin] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		w.Header().Add("Vary", "Origin")
		if origin != "" && (allowed[origin] || allowed["*"]) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusOK)
				return
			}
		} else if origin != "" && r.Method == http.MethodOptions {
			writeError(w, &apiError{http.StatusBadRequest, "Disallowed CORS origin"})
			return
		}
		// FastAPI accepts a trailing slash by redirecting to the canonical route.
		if r.URL.Path != "/" && strings.HasSuffix(r.URL.Path, "/") {
			target := strings.TrimRight(r.URL.Path, "/")
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			return
		}
		next.ServeHTTP(w, r)
	})
}
