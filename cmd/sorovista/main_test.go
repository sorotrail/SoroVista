package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorovista/internal/metrics"
	"github.com/sorotrail/sorovista/internal/source"
)

type stubSource struct{}

func (stubSource) ListContracts(context.Context, source.ContractQuery) (source.ContractPage, error) {
	return source.ContractPage{}, nil
}

func (stubSource) ListEvents(context.Context, source.EventQuery) (source.EventPage, error) {
	return source.EventPage{}, nil
}

func (stubSource) GetEvent(context.Context, string) (source.Event, error) {
	return source.Event{ID: "0000000100-0000000001"}, nil
}

func (stubSource) ContractStats(context.Context, string) (source.ContractStats, error) {
	return source.ContractStats{}, nil
}

func (stubSource) Stats(context.Context) (source.Stats, error) {
	return source.Stats{}, nil
}

func (stubSource) Status(context.Context) source.Status {
	return source.Status{Healthy: true}
}

func TestRouterRecordsChiRoutePattern(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.New()
	h := router(stubSource{}, log, m, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events/0000000100-0000000001", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	metricsRec := httptest.NewRecorder()
	h.ServeHTTP(metricsRec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, metricsRec.Code)

	// Chi fills the route pattern during matching, so Middleware must read
	// "/api/events/{id}" after next.ServeHTTP returns rather than recording
	// the raw event ID.
	body := metricsRec.Body.String()
	assert.Contains(t, body,
		`sorovista_http_request_duration_seconds_count{method="GET",route="/api/events/{id}",status="200"} 1`)
	assert.NotContains(t, body, `route="/api/events/0000000100-0000000001"`)
}

func TestRouterConcurrentCallsDoNotRace(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := metrics.New()
			h := router(stubSource{}, log, m, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))
			assert.Equal(t, http.StatusOK, rec.Code)
		}()
	}
	wg.Wait()
}
