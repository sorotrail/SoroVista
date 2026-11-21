package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sorotrail/sorovista/internal/config"
	"github.com/sorotrail/sorovista/internal/reqid"
	"github.com/sorotrail/sorovista/internal/source"
)

const (
	// MaxExportRows bounds a single export. Streaming removes the need to
	// buffer the result in memory, but an unbounded export would still let one
	// anonymous caller hold a connection open (and, in upstream mode, spend
	// SoroTrail's request budget) for an arbitrarily long time. A hard cap is
	// the simplest defensible bound; clients that need more can narrow the
	// filters or page through the JSON API.
	MaxExportRows = 100_000

	// exportFlushRows is how many rows are written between flushes. Flushing
	// keeps a slow query from looking like a hung response to a client that
	// reads incrementally.
	exportFlushRows = 500
)

// csvColumns is the stable CSV header. Changing or reordering it is a breaking
// change for spreadsheet consumers, so document any change.
var csvColumns = []string{
	"id",
	"contract_id",
	"ledger",
	"type",
	"tx_hash",
	"tx_index",
	"op_index",
	"in_successful_call",
	"topics",
	"value",
	"ledger_closed_at",
	"created_at",
}

// errExportLimit marks a normal stop at MaxExportRows rather than a failure.
var errExportLimit = errors.New("export row limit reached")

// handleExportEvents serves GET /api/events/export: the global event feed as a
// downloadable stream. It accepts the same filters as GET /api/events, plus
// contract_id (already documented for that endpoint) and format.
func (s *Server) handleExportEvents(w http.ResponseWriter, r *http.Request) {
	q, err := EventQueryFromRequest(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if cid := strings.TrimSpace(r.URL.Query().Get("contract_id")); cid != "" {
		if !config.ValidContractID(cid) {
			writeError(w, r, http.StatusBadRequest, fmt.Errorf("invalid contract ID %q", cid))
			return
		}
		q.ContractID = cid
	}
	s.exportEvents(w, r, q)
}

// handleExportContractEvents serves GET /api/contracts/{id}/events/export: one
// contract's history as a downloadable stream, with the contract fixed.
func (s *Server) handleExportContractEvents(w http.ResponseWriter, r *http.Request) {
	contractID := chi.URLParam(r, "id")
	if !config.ValidContractID(contractID) {
		writeError(w, r, http.StatusBadRequest, fmt.Errorf("invalid contract ID %q", contractID))
		return
	}

	q, err := EventQueryFromRequest(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	q.ContractID = contractID
	s.exportEvents(w, r, q)
}

// exportEvents streams every event matching q in the requested format.
//
// Format is a query parameter rather than an Accept header because the rest of
// the API already selects behaviour with query parameters, and a plain URL is
// easier to paste into a browser, curl or a spreadsheet tool than content
// negotiation.
func (s *Server) exportEvents(w http.ResponseWriter, r *http.Request, q source.EventQuery) {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "ndjson" {
		writeError(w, r, http.StatusBadRequest, fmt.Errorf("invalid format %q (want csv or ndjson)", format))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, http.StatusInternalServerError, errors.New("streaming not supported"))
		return
	}

	// Everything that can fail without touching the backend is validated
	// before the first write. Once a byte is out the status is committed, so a
	// later backend error can only truncate the stream — see docs/reference/api.md.
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	case "ndjson":
		w.Header().Set("Content-Type", "application/x-ndjson")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, exportFilename(q.ContractID, format)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	// A page of MaxLimit keeps the number of backend round-trips down. The
	// caller's limit is a JSON-pagination concern and is deliberately ignored:
	// an export covers every matching event up to MaxExportRows.
	q.Limit = source.MaxLimit
	q.Cursor = r.URL.Query().Get("cursor")

	var err error
	if format == "csv" {
		err = s.exportCSV(w, r.Context(), flusher, q)
	} else {
		err = s.exportNDJSON(w, r.Context(), flusher, q)
	}
	s.finishExport(r, err)
}

func (s *Server) exportCSV(w http.ResponseWriter, ctx context.Context, flusher http.Flusher, q source.EventQuery) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvColumns); err != nil {
		return err
	}

	rows := 0
	err := s.walkEvents(ctx, q, func(ev source.Event) error {
		if err := cw.Write(csvRow(ev)); err != nil {
			return err
		}
		rows++
		if rows%exportFlushRows == 0 {
			cw.Flush()
			if err := cw.Error(); err != nil {
				return err
			}
			// csv.Writer buffers, so flushing it only reaches the HTTP
			// response; the flusher is what pushes bytes to the client.
			flusher.Flush()
		}
		return nil
	})
	cw.Flush()
	flusher.Flush()
	if ferr := cw.Error(); ferr != nil && err == nil {
		err = ferr
	}
	return err
}

func (s *Server) exportNDJSON(w http.ResponseWriter, ctx context.Context, flusher http.Flusher, q source.EventQuery) error {
	enc := json.NewEncoder(w)

	rows := 0
	return s.walkEvents(ctx, q, func(ev source.Event) error {
		// Each line is the same annotated event object the JSON endpoints
		// return, with no wrapper and no trailing comma.
		if err := enc.Encode(annotatedEvent{Event: ev, Token: DetectTokenEvent(ev)}); err != nil {
			return err
		}
		rows++
		if rows%exportFlushRows == 0 {
			flusher.Flush()
		}
		return nil
	})
}

// walkEvents pages through the source's cursor API, handing each event to
// visit as it is read. Reusing ListEvents is what keeps export filters from
// diverging from the JSON endpoints; it also means both backends work without
// a second query path.
func (s *Server) walkEvents(ctx context.Context, q source.EventQuery, visit func(source.Event) error) error {
	remaining := MaxExportRows
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		page, err := s.src.ListEvents(ctx, q)
		if err != nil {
			return err
		}
		for _, ev := range page.Events {
			// Check cancellation per row, not just per page, so a client that
			// disconnects mid-page stops the remaining writes promptly.
			if err := ctx.Err(); err != nil {
				return err
			}
			if remaining == 0 {
				return errExportLimit
			}
			if err := visit(ev); err != nil {
				return err
			}
			remaining--
		}
		if page.NextCursor == "" || len(page.Events) == 0 {
			return nil
		}
		q.Cursor = page.NextCursor
	}
}

// csvRow renders one event. Topics and value are JSON documents, written as a
// JSON string inside the cell; they are not flattened because their shape is
// part of the data and a lossy projection would be worse than a parse step.
// Nil topics or value become an empty cell.
//
// Values are deliberately not escaped against spreadsheet formula injection
// (a leading "=", "+", "-" or "@"): contract events are data, and rewriting
// them would corrupt legitimate values. The Content-Disposition attachment
// and nosniff headers stop a browser rendering the file in place; consumers
// importing untrusted data should apply their own sanitisation.
func csvRow(ev source.Event) []string {
	return []string{
		ev.ID,
		ev.ContractID,
		strconv.FormatInt(ev.Ledger, 10),
		ev.Type,
		ev.TxHash,
		strconv.FormatInt(int64(ev.TxIndex), 10),
		strconv.FormatInt(int64(ev.OpIndex), 10),
		strconv.FormatBool(ev.InSuccessfulCall),
		string(ev.Topics),
		string(ev.Value),
		formatExportTime(ev.LedgerClosedAt),
		formatExportTime(ev.CreatedAt),
	}
}

func formatExportTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func exportFilename(contractID, format string) string {
	scope := "events"
	if contractID != "" {
		scope = contractID + "-events"
	}
	return fmt.Sprintf("sorovista-%s.%s", scope, format)
}

// finishExport logs anything that ended the stream early. A backend failure or
// a client disconnect cannot change the already-sent 200 status, so the
// documented outcome is a truncated file: complete rows only, no terminator
// beyond the last row.
func (s *Server) finishExport(r *http.Request, err error) {
	switch {
	case err == nil, errors.Is(err, errExportLimit):
		return
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The client went away; the request context stopped the write loop.
		return
	default:
		s.log.Error("exporting events failed", "request_id", reqid.From(r), "error", err)
	}
}
