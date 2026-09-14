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
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func alwaysReady(context.Context) error { return nil }

func TestHealthz(t *testing.T) {
	router := NewRouter(discardLogger(), alwaysReady)

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
	router := NewRouter(discardLogger(), alwaysReady)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestReadyz(t *testing.T) {
	router := NewRouter(discardLogger(), alwaysReady)

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
	router := NewRouter(discardLogger(), notReady)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestUnknownRoute(t *testing.T) {
	router := NewRouter(discardLogger(), alwaysReady)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
