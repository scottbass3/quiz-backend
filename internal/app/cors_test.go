package app

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestParseOrigins(t *testing.T) {
	got := parseOrigins(" https://a.example.com/ , http://localhost:5173,, ")
	want := []string{"https://a.example.com", "http://localhost:5173"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if parseOrigins("") != nil {
		t.Fatal("empty setting should give no origins")
	}
}

func TestCORSMiddleware(t *testing.T) {
	reached := false
	h := corsMiddleware([]string{"https://ui.example.com"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	do := func(method, origin string, preflight bool) *httptest.ResponseRecorder {
		reached = false
		r := httptest.NewRequest(method, "/games", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if preflight {
			r.Header.Set("Access-Control-Request-Method", "POST")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	rec := do(http.MethodOptions, "https://ui.example.com", true)
	if rec.Code != http.StatusNoContent || reached {
		t.Fatalf("preflight: expected 204 answered by the middleware, got %d (reached=%v)", rec.Code, reached)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://ui.example.com" ||
		rec.Header().Get("Access-Control-Allow-Credentials") != "true" ||
		rec.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Fatalf("preflight: missing CORS headers %v", rec.Header())
	}

	rec = do(http.MethodGet, "https://ui.example.com", false)
	if !reached || rec.Header().Get("Access-Control-Allow-Origin") != "https://ui.example.com" {
		t.Fatalf("allowed origin: expected headers and pass-through, got %v", rec.Header())
	}

	rec = do(http.MethodGet, "https://evil.example.com", false)
	if !reached || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("other origin: expected no CORS headers, got %v", rec.Header())
	}

	// Disabled: no headers at all.
	off := corsMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest(http.MethodGet, "/games", nil)
	r.Header.Set("Origin", "https://ui.example.com")
	rec = httptest.NewRecorder()
	off.ServeHTTP(rec, r)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("disabled middleware must not add CORS headers")
	}
}
