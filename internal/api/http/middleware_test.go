package http

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func bufferLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, nil))
}

// decodeLogLines parses newline-delimited JSON log records into maps, in
// case a middleware chain emits more than one line for a single request.
func decodeLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	var lines []map[string]any
	for raw := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("decode log line %q: %v", raw, err)
		}
		lines = append(lines, line)
	}
	return lines
}

func TestLoggingMiddleware(t *testing.T) {
	var buf bytes.Buffer
	logger := bufferLogger(&buf)

	handler := loggingMiddleware(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/some/path", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	lines := decodeLogLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want 1: %v", len(lines), lines)
	}

	line := lines[0]
	if line["method"] != http.MethodGet {
		t.Errorf("method = %v, want %v", line["method"], http.MethodGet)
	}
	if line["path"] != "/some/path" {
		t.Errorf("path = %v, want %v", line["path"], "/some/path")
	}
	if status, ok := line["status"].(float64); !ok || int(status) != http.StatusCreated {
		t.Errorf("status = %v, want %v", line["status"], http.StatusCreated)
	}
	if _, ok := line["duration_ms"]; !ok {
		t.Errorf("log line missing duration_ms: %v", line)
	}
}

func TestLoggingMiddlewareDefaultStatus(t *testing.T) {
	var buf bytes.Buffer
	logger := bufferLogger(&buf)

	// A handler that never calls WriteHeader implicitly sends 200 — the
	// recorder must report that, not a zero value.
	handler := loggingMiddleware(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	lines := decodeLogLines(t, &buf)
	status, _ := lines[0]["status"].(float64)
	if int(status) != http.StatusOK {
		t.Fatalf("status = %v, want %v", lines[0]["status"], http.StatusOK)
	}
}

func TestRecoverMiddleware(t *testing.T) {
	var buf bytes.Buffer
	logger := bufferLogger(&buf)

	handler := recoverMiddleware(logger, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/panics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	var body map[string]map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"]["code"] != "internal_error" {
		t.Fatalf("error.code = %q, want %q", body["error"]["code"], "internal_error")
	}

	lines := decodeLogLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want 1: %v", len(lines), lines)
	}
	if lines[0]["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", lines[0]["level"])
	}
	if lines[0]["panic"] != "boom" {
		t.Errorf("panic = %v, want %q", lines[0]["panic"], "boom")
	}
}

func TestCORSMiddlewareReflectsOrigin(t *testing.T) {
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/quotes/latest", nil)
	req.Header.Set("Origin", "http://localhost:8081")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:8081" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the request's Origin", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want %q", got, "Origin")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (request must still reach the handler)", rec.Code, http.StatusOK)
	}
}

func TestCORSMiddlewareNoOriginHeaderIsUnaffected(t *testing.T) {
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/quotes/latest", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for a same-origin request", got)
	}
}

func TestCORSMiddlewareAnswersPreflightWithoutReachingHandler(t *testing.T) {
	called := false
	handler := corsMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/api/v1/quotes/updates", nil)
	req.Header.Set("Origin", "http://localhost:8081")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if called {
		t.Error("preflight OPTIONS must be answered directly, never forwarded to the next handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("Access-Control-Allow-Methods missing from preflight response")
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Error("Access-Control-Allow-Headers missing from preflight response")
	}
}

func TestRequestIDMiddlewareGeneratesIDWhenAbsent(t *testing.T) {
	var gotID string
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = requestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotID == "" {
		t.Fatal("requestIDFromContext returned empty, want a generated id")
	}
	if got := rec.Header().Get(requestIDHeader); got != gotID {
		t.Errorf("%s header = %q, want it to match the context id %q", requestIDHeader, got, gotID)
	}
}

func TestRequestIDMiddlewareTrustsInboundHeader(t *testing.T) {
	const inbound = "upstream-assigned-id"
	var gotID string
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = requestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set(requestIDHeader, inbound)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotID != inbound {
		t.Errorf("context id = %q, want the inbound id %q trusted as-is", gotID, inbound)
	}
	if got := rec.Header().Get(requestIDHeader); got != inbound {
		t.Errorf("%s header = %q, want %q echoed back", requestIDHeader, got, inbound)
	}
}

func TestRecoverMiddlewareNoPanic(t *testing.T) {
	var buf bytes.Buffer
	logger := bufferLogger(&buf)

	handler := recoverMiddleware(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no log output, got %q", buf.String())
	}
}
