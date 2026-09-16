package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/provider/fake"
	"github.com/djsega1/plata-test-task/internal/storage/memory"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
	"github.com/djsega1/plata-test-task/pkg/clock"
)

var testNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func testPair(t *testing.T) domainquotes.CurrencyPair {
	t.Helper()
	pair, err := domainquotes.NewCurrencyPair(domainquotes.CodeEUR, domainquotes.CodeMXN)
	require.NoError(t, err)
	return pair
}

func testRate(t *testing.T, at time.Time) domainquotes.CurrencyRate {
	t.Helper()
	rate, err := domainquotes.NewCurrencyRate(
		decimal.RequireFromString("18.4321"), false, "live", "exchangerate.dev",
		true, at, at, at.Add(time.Minute),
	)
	require.NoError(t, err)
	return rate
}

// newBusinessRouter builds a router over a fresh storage/memory repository
// and a fake clock — no dispatcher, so a request stays pending until a test
// resolves it itself, either directly through repo or via a Dispatcher it
// wires up separately (see TestEndToEnd_PostPollLatest).
func newBusinessRouter(t *testing.T) (http.Handler, *memory.Repository, *clock.FakeClock) {
	t.Helper()
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(testNow)
	router := NewRouter(discardLogger(), alwaysReady, repo, fc, nil)
	return router, repo, fc
}

func doJSON(t *testing.T, router http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		r = httptest.NewRequestWithContext(t.Context(), method, target, bytes.NewReader(b))
	} else {
		r = httptest.NewRequestWithContext(t.Context(), method, target, nil)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v))
	return v
}

// wireQuote mirrors updateStatusResponse/latestQuoteResponse with every
// field exported and flattened, since encoding/json can marshal — but not
// unmarshal — an embedded pointer field of an unexported type (*quoteRateDTO):
// reflect refuses to set it regardless of package. Production code never
// unmarshals its own response, so this is purely a test-decoding concern.
type wireQuote struct {
	UpdateID   string     `json:"update_id"`
	Pair       string     `json:"pair"`
	Status     string     `json:"status"`
	Price      string     `json:"price"`
	QuotedAt   time.Time  `json:"quoted_at"`
	FetchedAt  time.Time  `json:"fetched_at"`
	Provider   string     `json:"provider"`
	Quality    string     `json:"quality"`
	Derived    bool       `json:"derived"`
	Indicative bool       `json:"indicative"`
	Cached     *bool      `json:"cached"`
	Attempts   int        `json:"attempts"`
	Error      *errorBody `json:"error"`
}

// --- POST /api/v1/quotes/updates ---

func TestPostQuotesUpdates_Accepted(t *testing.T) {
	router, _, _ := newBusinessRouter(t)

	rec := doJSON(t, router, http.MethodPost, "/api/v1/quotes/updates", map[string]string{"pair": "EUR/MXN"})

	require.Equal(t, http.StatusAccepted, rec.Code)
	body := decodeBody[createUpdateResponse](t, rec)
	assert.NotEmpty(t, body.UpdateID)
	assert.Equal(t, "EUR/MXN", body.Pair)
	assert.Equal(t, "pending", body.Status)
}

func TestPostQuotesUpdates_MalformedJSON(t *testing.T) {
	router, _, _ := newBusinessRouter(t)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/quotes/updates", bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", decodeBody[errorEnvelope](t, rec).Error.Code)
}

func TestPostQuotesUpdates_MissingPair(t *testing.T) {
	router, _, _ := newBusinessRouter(t)

	rec := doJSON(t, router, http.MethodPost, "/api/v1/quotes/updates", map[string]string{})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", decodeBody[errorEnvelope](t, rec).Error.Code)
}

func TestPostQuotesUpdates_UnsupportedPair(t *testing.T) {
	router, _, _ := newBusinessRouter(t)

	rec := doJSON(t, router, http.MethodPost, "/api/v1/quotes/updates", map[string]string{"pair": "EUR/GBP"})

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "unsupported_pair", decodeBody[errorEnvelope](t, rec).Error.Code)
}

// TestPostQuotesUpdates_MalformedPair is not a well-formed pair at all (no
// separator) — distinct from TestPostQuotesUpdates_UnsupportedPair's
// well-formed-but-disallowed EUR/GBP: 400, not 422.
func TestPostQuotesUpdates_MalformedPair(t *testing.T) {
	router, _, _ := newBusinessRouter(t)

	rec := doJSON(t, router, http.MethodPost, "/api/v1/quotes/updates", map[string]string{"pair": "EURMXN"})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", decodeBody[errorEnvelope](t, rec).Error.Code)
}

func TestPostQuotesUpdates_IdempotencyReplay(t *testing.T) {
	router, _, _ := newBusinessRouter(t)

	r1 := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/quotes/updates",
		bytes.NewReader(mustJSON(t, map[string]string{"pair": "EUR/MXN"})))
	r1.Header.Set("Idempotency-Key", "key-1")
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, r1)
	require.Equal(t, http.StatusAccepted, rec1.Code)
	first := decodeBody[createUpdateResponse](t, rec1)

	r2 := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/quotes/updates",
		bytes.NewReader(mustJSON(t, map[string]string{"pair": "EUR/MXN"})))
	r2.Header.Set("Idempotency-Key", "key-1")
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, r2)

	require.Equal(t, http.StatusOK, rec2.Code)
	assert.Equal(t, "true", rec2.Header().Get("Idempotency-Replayed"))
	second := decodeBody[createUpdateResponse](t, rec2)
	assert.Equal(t, first.UpdateID, second.UpdateID)
}

func TestPostQuotesUpdates_IdempotencyKeyTooLong(t *testing.T) {
	router, _, _ := newBusinessRouter(t)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/quotes/updates",
		bytes.NewReader(mustJSON(t, map[string]string{"pair": "EUR/MXN"})))
	r.Header.Set("Idempotency-Key", strings.Repeat("a", 256))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", decodeBody[errorEnvelope](t, rec).Error.Code)
}

func TestPostQuotesUpdates_IdempotencyConflict(t *testing.T) {
	router, _, _ := newBusinessRouter(t)

	r1 := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/quotes/updates",
		bytes.NewReader(mustJSON(t, map[string]string{"pair": "EUR/MXN"})))
	r1.Header.Set("Idempotency-Key", "key-1")
	router.ServeHTTP(httptest.NewRecorder(), r1)

	r2 := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/quotes/updates",
		bytes.NewReader(mustJSON(t, map[string]string{"pair": "USD/MXN"})))
	r2.Header.Set("Idempotency-Key", "key-1")
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, r2)

	assert.Equal(t, http.StatusConflict, rec2.Code)
	assert.Equal(t, "idempotency_conflict", decodeBody[errorEnvelope](t, rec2).Error.Code)
}

// TestPostQuotesUpdates_LogsRequestIDWithUpdateID covers the one log line
// that ties request_id to update_id, so a request_id from an access log or
// client report can be traced forward into the worker's update_id-keyed logs.
func TestPostQuotesUpdates_LogsRequestIDWithUpdateID(t *testing.T) {
	var buf bytes.Buffer
	logger := bufferLogger(&buf)
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(testNow)
	router := NewRouter(logger, alwaysReady, repo, fc, nil)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/quotes/updates",
		bytes.NewReader(mustJSON(t, map[string]string{"pair": "EUR/MXN"})))
	req.Header.Set(requestIDHeader, "test-request-id")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)
	created := decodeBody[createUpdateResponse](t, rec)

	var pivot map[string]any
	for _, line := range decodeLogLines(t, &buf) {
		if line["msg"] == "update requested" {
			pivot = line
			break
		}
	}
	require.NotNil(t, pivot, "expected an \"update requested\" log line")
	assert.Equal(t, "test-request-id", pivot["request_id"])
	assert.Equal(t, created.UpdateID, pivot["update_id"])
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// --- GET /api/v1/quotes/updates/{id} ---

func TestGetQuotesUpdate_Pending(t *testing.T) {
	router, repo, fc := newBusinessRouter(t)
	pair := testPair(t)
	req := domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now())
	require.NoError(t, repo.CreateUpdateRequest(t.Context(), req, ""))

	rec := doJSON(t, router, http.MethodGet, "/api/v1/quotes/updates/"+req.ID.String(), nil)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
	body := decodeBody[wireQuote](t, rec)
	assert.Equal(t, "pending", body.Status)
	assert.Empty(t, body.Price)
}

func TestGetQuotesUpdate_Succeeded(t *testing.T) {
	router, repo, fc := newBusinessRouter(t)
	pair := testPair(t)
	req := domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now())
	require.NoError(t, repo.CreateUpdateRequest(t.Context(), req, ""))
	_, err := repo.ClaimBatch(t.Context(), 10, fc.Now(), fc.Now().Add(-time.Hour))
	require.NoError(t, err)

	rate := testRate(t, fc.Now())
	require.NoError(t, repo.CompleteSuccess(t.Context(), req.ID, pair, rate, fc.Now()))

	rec := doJSON(t, router, http.MethodGet, "/api/v1/quotes/updates/"+req.ID.String(), nil)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Retry-After"))
	body := decodeBody[wireQuote](t, rec)
	assert.Equal(t, "succeeded", body.Status)
	assert.Equal(t, "18.4321000000", body.Price)
	assert.Equal(t, "exchangerate.dev", body.Provider)
	require.NotNil(t, body.Cached)
	assert.False(t, *body.Cached, "CompleteSuccess is a real provider fetch, not a cache hit")
}

// TestGetQuotesUpdate_SucceededReuse covers the other half of "cached": a
// request resolved via CompleteSuccessReuse (a still-fresh quote, no
// provider call) must report cached:true so a client can tell it apart from
// a request that actually triggered a refresh.
func TestGetQuotesUpdate_SucceededReuse(t *testing.T) {
	router, repo, fc := newBusinessRouter(t)
	pair := testPair(t)

	first := domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now())
	require.NoError(t, repo.CreateUpdateRequest(t.Context(), first, ""))
	second := domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now())
	require.NoError(t, repo.CreateUpdateRequest(t.Context(), second, ""))
	_, err := repo.ClaimBatch(t.Context(), 10, fc.Now(), fc.Now().Add(-time.Hour))
	require.NoError(t, err)

	rate := testRate(t, fc.Now())
	require.NoError(t, repo.CompleteSuccess(t.Context(), first.ID, pair, rate, fc.Now()))
	_, quoteID, err := repo.GetLatestQuote(t.Context(), pair)
	require.NoError(t, err)
	require.NoError(t, repo.CompleteSuccessReuse(t.Context(), second.ID, quoteID, fc.Now()))

	rec := doJSON(t, router, http.MethodGet, "/api/v1/quotes/updates/"+second.ID.String(), nil)

	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody[wireQuote](t, rec)
	assert.Equal(t, "succeeded", body.Status)
	require.NotNil(t, body.Cached)
	assert.True(t, *body.Cached)
}

func TestGetQuotesUpdate_Failed(t *testing.T) {
	router, repo, fc := newBusinessRouter(t)
	pair := testPair(t)
	req := domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now())
	require.NoError(t, repo.CreateUpdateRequest(t.Context(), req, ""))
	_, err := repo.ClaimBatch(t.Context(), 10, fc.Now(), fc.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.NoError(t, repo.CompleteFailure(t.Context(), req.ID, "unsupported_pair", "no rate for pair", false, time.Time{}, fc.Now()))

	rec := doJSON(t, router, http.MethodGet, "/api/v1/quotes/updates/"+req.ID.String(), nil)

	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody[wireQuote](t, rec)
	assert.Equal(t, "failed", body.Status)
	assert.Equal(t, 1, body.Attempts)
	require.NotNil(t, body.Error)
	assert.Equal(t, "unsupported_pair", body.Error.Code)
	// The wire message is a fixed, safe-by-code string, not the raw
	// error_message stored in the row (see dto.go's safeErrorMessage) — a
	// raw message can carry internal detail (URLs, dial errors) that
	// shouldn't reach a client.
	assert.Equal(t, "no rate available for this pair", body.Error.Message)
}

func TestGetQuotesUpdate_NotFound(t *testing.T) {
	router, _, _ := newBusinessRouter(t)

	rec := doJSON(t, router, http.MethodGet, "/api/v1/quotes/updates/"+uuidV7(t), nil)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "not_found", decodeBody[errorEnvelope](t, rec).Error.Code)
}

func TestGetQuotesUpdate_MalformedID(t *testing.T) {
	router, _, _ := newBusinessRouter(t)

	rec := doJSON(t, router, http.MethodGet, "/api/v1/quotes/updates/not-a-uuid", nil)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- GET /api/v1/quotes/latest ---

func TestGetQuotesLatest_Found(t *testing.T) {
	router, repo, fc := newBusinessRouter(t)
	pair := testPair(t)
	req := domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now())
	require.NoError(t, repo.CreateUpdateRequest(t.Context(), req, ""))
	_, err := repo.ClaimBatch(t.Context(), 10, fc.Now(), fc.Now().Add(-time.Hour))
	require.NoError(t, err)
	rate := testRate(t, fc.Now())
	require.NoError(t, repo.CompleteSuccess(t.Context(), req.ID, pair, rate, fc.Now()))

	rec := doJSON(t, router, http.MethodGet, "/api/v1/quotes/latest?pair=EUR/MXN", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody[wireQuote](t, rec)
	assert.Equal(t, "EUR/MXN", body.Pair)
	assert.Equal(t, "18.4321000000", body.Price)
}

func TestGetQuotesLatest_NotFound(t *testing.T) {
	router, _, _ := newBusinessRouter(t)

	rec := doJSON(t, router, http.MethodGet, "/api/v1/quotes/latest?pair=EUR/MXN", nil)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "not_found", decodeBody[errorEnvelope](t, rec).Error.Code)
}

func TestGetQuotesLatest_MissingPair(t *testing.T) {
	router, _, _ := newBusinessRouter(t)

	rec := doJSON(t, router, http.MethodGet, "/api/v1/quotes/latest", nil)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", decodeBody[errorEnvelope](t, rec).Error.Code)
}

func TestGetQuotesLatest_UnsupportedPair(t *testing.T) {
	router, _, _ := newBusinessRouter(t)

	rec := doJSON(t, router, http.MethodGet, "/api/v1/quotes/latest?pair=EUR/GBP", nil)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "unsupported_pair", decodeBody[errorEnvelope](t, rec).Error.Code)
}

// TestGetQuotesLatest_MalformedPair is not a well-formed pair at all (no
// separator) — distinct from TestGetQuotesLatest_UnsupportedPair's
// well-formed-but-disallowed EUR/GBP: 400, not 422.
func TestGetQuotesLatest_MalformedPair(t *testing.T) {
	router, _, _ := newBusinessRouter(t)

	rec := doJSON(t, router, http.MethodGet, "/api/v1/quotes/latest?pair=EURMXN", nil)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", decodeBody[errorEnvelope](t, rec).Error.Code)
}

func uuidV7(t *testing.T) string {
	t.Helper()
	return uuid.NewV7().String()
}

// --- end-to-end: POST -> poll -> latest ---

// TestEndToEnd_PostPollLatest drives the real production path — the POST
// handler's nudge, then a Dispatcher/Worker/RateLimiter stack over the same
// storage/memory repository and a provider/fake — instead of writing to the
// repository directly like the tests above. ClaimAndDispatch is called
// synchronously once, standing in for cmd/server's ticker/nudge-driven
// goroutine: this proves the state machine end to end without a real timer
// or time.Sleep (both are unnecessary here and the latter is disallowed).
func TestEndToEnd_PostPollLatest(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(testNow)
	nudge := make(chan struct{}, 1)
	router := NewRouter(discardLogger(), alwaysReady, repo, fc, nudge)

	provider := fake.NewFakeRateProvider(fc, 0, 0, 0, time.Minute)
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5, time.Hour, time.Minute)
	dispatcher := quotes.NewDispatcher(repo, fc, worker, discardLogger(), 10, 4, time.Hour, time.Minute)

	postRec := doJSON(t, router, http.MethodPost, "/api/v1/quotes/updates", map[string]string{"pair": "EUR/MXN"})
	require.Equal(t, http.StatusAccepted, postRec.Code)
	created := decodeBody[createUpdateResponse](t, postRec)
	assert.Equal(t, "pending", created.Status)

	select {
	case <-nudge:
	default:
		t.Fatal("POST did not nudge the dispatcher")
	}

	pollRec := doJSON(t, router, http.MethodGet, "/api/v1/quotes/updates/"+created.UpdateID, nil)
	require.Equal(t, http.StatusOK, pollRec.Code)
	assert.Equal(t, "pending", decodeBody[wireQuote](t, pollRec).Status)

	_, err := dispatcher.ClaimAndDispatch(t.Context())
	require.NoError(t, err)

	pollRec2 := doJSON(t, router, http.MethodGet, "/api/v1/quotes/updates/"+created.UpdateID, nil)
	require.Equal(t, http.StatusOK, pollRec2.Code)
	done := decodeBody[wireQuote](t, pollRec2)
	require.Equal(t, "succeeded", done.Status)
	assert.NotEmpty(t, done.Price)

	latestRec := doJSON(t, router, http.MethodGet, "/api/v1/quotes/latest?pair=EUR/MXN", nil)
	require.Equal(t, http.StatusOK, latestRec.Code)
	latest := decodeBody[wireQuote](t, latestRec)
	assert.Equal(t, done.Price, latest.Price)
	assert.Equal(t, done.Provider, latest.Provider)
}
