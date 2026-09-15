package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/djsega1/plata-test-task/internal/storage/memory"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func alwaysReady(context.Context) error { return nil }

// healthzRouter builds a router for the health-check tests below, which
// don't exercise the business endpoints and so don't care what backs them.
func healthzRouter(ready func(context.Context) error) http.Handler {
	return NewRouter(discardLogger(), ready, memory.NewRepository(), nil, nil)
}

func TestHealthz(t *testing.T) {
	router := healthzRouter(alwaysReady)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "ok", body["status"])
}

func TestHealthzWrongMethod(t *testing.T) {
	router := healthzRouter(alwaysReady)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestReadyz(t *testing.T) {
	router := healthzRouter(alwaysReady)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "ready", body["status"])
}

func TestReadyzNotReady(t *testing.T) {
	notReady := func(context.Context) error { return errors.New("db unreachable") }
	router := healthzRouter(notReady)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestUnknownRoute(t *testing.T) {
	router := healthzRouter(alwaysReady)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
