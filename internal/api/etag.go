package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/sorotrail/sorovista/internal/buildinfo"
	"github.com/sorotrail/sorovista/internal/source"
)

// The ETag implementation lives here rather than in middleware because a
// useful tag depends on what the backend returned, not on the response bytes:
// hashing the serialised body would still pay the query, which is most of a
// request's cost. Each handler therefore builds a tag from the query it ran
// and the change-relevant fields of the result.
//
// The tags are strong: every field in the body of these endpoints comes from
// immutable event rows or from aggregates over them, and nothing embeds the
// current time. A tag that changed on every request would make a strong ETag a
// lie, so keep it that way if these responses grow new fields.

// etagFor hashes the request identity plus a fingerprint of the result. The
// query string is part of the hash so two different filters can never collide
// on one tag and serve a wrong 304. The build version is included as well:
// a deploy that changes how a response is rendered must invalidate clients'
// stored tags.
func etagFor(route string, r *http.Request, fingerprint ...string) string {
	h := sha256.New()
	write := func(s string) {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	write(buildinfo.Version)
	write(route)
	write(r.URL.Query().Encode())
	for _, s := range fingerprint {
		write(s)
	}
	return `"` + hex.EncodeToString(h.Sum(nil)) + `"`
}

// eventPageETag derives a tag from every event ID on the page plus the paging
// state. A new event in range changes the newest ID; a backfilled one changes
// the count or the set of IDs. Both are conservative, which is what matters:
// a stale tag that misses new data is a bug, a tag that changes too often is
// only a lost cache hit.
func eventPageETag(r *http.Request, page source.EventPage) string {
	fingerprint := make([]string, 0, len(page.Events)+2)
	fingerprint = append(fingerprint, strconv.Itoa(len(page.Events)), page.NextCursor)
	for _, e := range page.Events {
		fingerprint = append(fingerprint, e.ID)
	}
	return etagFor("events", r, fingerprint...)
}

// contractPageETag includes the aggregate fields the contracts list renders,
// not just the IDs: a contract emitting another event changes its event count
// and activity without changing which contracts are on the page.
func contractPageETag(r *http.Request, page source.ContractPage) string {
	fingerprint := make([]string, 0, len(page.Contracts)+2)
	fingerprint = append(fingerprint, strconv.Itoa(len(page.Contracts)), page.NextCursor)
	for _, c := range page.Contracts {
		fingerprint = append(fingerprint, fmt.Sprintf("%s|%d|%d|%d|%d",
			c.ID, c.EventCount, c.FirstLedger, c.LastLedger, c.LastActivity.UnixNano()))
	}
	return etagFor("contracts", r, fingerprint...)
}

// eventETag covers the single-event read. An event row is immutable once
// stored, so its ID and ingest time identify the body; the decoder version is
// covered by the build version in etagFor.
func eventETag(r *http.Request, e source.Event) string {
	return etagFor("event", r, e.ID, e.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"))
}

// writeConditionalJSON writes v with its ETag, or answers 304 when the client
// already holds this version. On a 304 the ETag is repeated as the HTTP
// specification requires, and no Content-Type is set because there is no body.
func writeConditionalJSON(w http.ResponseWriter, r *http.Request, etag string, status int, v any) {
	w.Header().Set("ETag", etag)
	if ifNoneMatchSatisfied(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, status, v)
}

// ifNoneMatchSatisfied reports whether an If-None-Match header names etag.
// The header is a comma-separated list, may be "*", and uses weak comparison,
// so a W/ prefix is ignored on either side.
func ifNoneMatchSatisfied(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(etag, "W/") {
			return true
		}
	}
	return false
}
