package metrics

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type flushableRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushableRecorder) Flush() {
	f.flushed = true
	f.ResponseRecorder.Flush()
}

type nonFlushableRecorder struct {
	header http.Header
	status int
}

func (n *nonFlushableRecorder) Header() http.Header {
	if n.header == nil {
		n.header = make(http.Header)
	}
	return n.header
}

func (n *nonFlushableRecorder) Write(b []byte) (int, error) {
	return len(b), nil
}

func (n *nonFlushableRecorder) WriteHeader(statusCode int) {
	n.status = statusCode
}

func TestStatusRecorderFlush(t *testing.T) {
	tests := []struct {
		name        string
		rw          http.ResponseWriter
		wantFlushed bool
	}{
		{
			name: "happy path forwards flush when underlying implements http.Flusher",
			rw: &flushableRecorder{
				ResponseRecorder: httptest.NewRecorder(),
			},
			wantFlushed: true,
		},
		{
			name:        "error or unsupported path does not panic when underlying is not http.Flusher",
			rw:          &nonFlushableRecorder{},
			wantFlushed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &statusRecorder{
				ResponseWriter: tt.rw,
				status:         http.StatusOK,
			}

			// Calling Flush should succeed safely in both cases
			rec.Flush()

			if tt.wantFlushed {
				f, ok := tt.rw.(*flushableRecorder)
				require.True(t, ok)
				assert.True(t, f.flushed, "expected underlying recorder Flush to be called")
			}
		})
	}
}

func TestStatusRecorderWriteHeader(t *testing.T) {
	underlying := httptest.NewRecorder()
	rec := &statusRecorder{
		ResponseWriter: underlying,
		status:         http.StatusOK,
	}

	rec.WriteHeader(http.StatusTeapot)
	assert.Equal(t, http.StatusTeapot, rec.status)
	assert.Equal(t, http.StatusTeapot, underlying.Code)
}

func TestStatusRecorderUnwrap(t *testing.T) {
	underlying := httptest.NewRecorder()
	rec := &statusRecorder{
		ResponseWriter: underlying,
		status:         http.StatusOK,
	}

	assert.Same(t, underlying, rec.Unwrap())
}

// failingCollector causes a gathering error when collected by not sending descriptors in Describe
// (making it an unchecked collector) and sending an invalid metric in Collect.
type failingCollector struct{}

func (f failingCollector) Describe(ch chan<- *prometheus.Desc) {
	// Send no descriptors so the collector is treated as unchecked
}

func (f failingCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.NewInvalidMetric(
		prometheus.NewInvalidDesc(errors.New("gather failed")),
		errors.New("gather failed"),
	)
}

func TestHandler(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*Metrics)
		wantStatus int
		wantBody   []string
	}{
		{
			name: "happy path serves registered prometheus metrics",
			setup: func(m *Metrics) {
				m.RecordPoll(true, 100*time.Millisecond)
				m.RecordEvents(5)
			},
			wantStatus: http.StatusOK,
			wantBody: []string{
				"sorovista_polls_total",
				"sorovista_poll_duration_seconds",
				"sorovista_events_ingested_total 5",
			},
		},
		{
			name: "error path returns 500 when collector fails gathering",
			setup: func(m *Metrics) {
				err := m.registry.Register(failingCollector{})
				if err != nil {
					panic(err)
				}
			},
			wantStatus: http.StatusInternalServerError,
			wantBody: []string{
				"An error has occurred while serving metrics",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New()
			if tt.setup != nil {
				tt.setup(m)
			}

			handler := m.Handler()
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			bodyBytes, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			bodyStr := string(bodyBytes)

			for _, expected := range tt.wantBody {
				assert.True(t, strings.Contains(bodyStr, expected), "expected body to contain %q, got: %s", expected, bodyStr)
			}
		})
	}
}
