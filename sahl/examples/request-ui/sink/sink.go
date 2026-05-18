// Package sink provides a Postgres-backed request log store with an SSE
// broadcaster and a JSON HTTP API for the request-ui example.
//
// Architecture:
//
//	Filter (Envoy worker thread)
//	  -> records channel (buffered, 4096)
//	  -> writer goroutine (batch INSERT every 100ms or 100 records)
//	     -> Postgres (DSN from REQUI_DSN env)
//	     -> broadcaster (fan-out to connected SSE clients)
//	  -> HTTP server (port from REQUI_ADDR env, default 0.0.0.0:6062)
//	       GET /api/requests              last 500 records, newest first
//	       GET /api/requests?since=ID     records with id > ID
//	       GET /api/requests?q=TEXT       full-text search
//	       GET /api/requests?errors=1     only error records
//	       GET /api/stream                SSE: new records in real time
//	       GET /                          embedded UI
package sink

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Record is the per-request data written by the filter and stored in Postgres.
type Record struct {
	ID        int64  `json:"id"`
	Timestamp string `json:"timestamp"`
	HasError  bool   `json:"has_error"`

	RequestID      string `json:"request_id"`
	Method         string `json:"method"`
	Path           string `json:"path"`
	Host           string `json:"host"`
	TraceID        string `json:"trace_id"`
	SpanID         string `json:"span_id"`
	RequestHeaders string `json:"request_headers"`
	RequestBody    string `json:"request_body"`

	UpstreamStatus  string `json:"upstream_status"`
	UpstreamAddress string `json:"upstream_address"`
	ResponseHeaders string `json:"response_headers"`
	ResponseBody    string `json:"response_body"`

	ErrorDetails        string `json:"error_details"`
	ResponseFlags       string `json:"response_flags"`
	ResponseCodeDetails string `json:"response_code_details"`
	UpstreamFailure     string `json:"upstream_failure"`

	DurationMs        float64 `json:"duration_ms"`
	RequestSizeBytes  float64 `json:"request_size_bytes"`
	ResponseSizeBytes float64 `json:"response_size_bytes"`
	ResponseCode      float64 `json:"response_code"`
}

// Sink is the central store and broadcaster.
type Sink struct {
	ch    chan *Record
	pool  *pgxpool.Pool
	bcast *broadcaster
	srv   *http.Server
	once  sync.Once
}

// New creates a Sink. Call Start() to connect and begin serving.
func New() *Sink {
	return &Sink{
		ch:    make(chan *Record, 4096),
		bcast: newBroadcaster(),
	}
}

// Send enqueues a record. Non-blocking: drops silently if the channel is full
// so it never stalls an Envoy worker thread.
func (s *Sink) Send(r *Record) {
	select {
	case s.ch <- r:
	default:
	}
}

// Start connects to Postgres, runs the schema migration, starts the writer
// goroutine and the HTTP server.
//
// Environment variables:
//
//	REQUI_DSN   Postgres DSN (default: postgres://requi:requi@localhost:5432/requi)
//	REQUI_ADDR  HTTP listen address (default: 0.0.0.0:6062)
func (s *Sink) Start() {
	s.once.Do(func() {
		dsn := os.Getenv("REQUI_DSN")
		if dsn == "" {
			dsn = "postgres://requi:requi@localhost:5432/requi?sslmode=disable"
		}
		addr := os.Getenv("REQUI_ADDR")
		if addr == "" {
			addr = "0.0.0.0:6062"
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[request-ui] pgx connect: %v\n", err)
			return
		}
		s.pool = pool

		if err := migrate(ctx, pool); err != nil {
			fmt.Fprintf(os.Stderr, "[request-ui] migrate: %v\n", err)
			return
		}

		go s.writer()

		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[request-ui] listen %s: %v\n", addr, err)
			return
		}

		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/requests", s.handleRequests)
		mux.HandleFunc("GET /api/stream", s.handleStream)
		mux.HandleFunc("/", handleUI)

		s.srv = &http.Server{Handler: mux}
		go s.srv.Serve(ln) //nolint:errcheck
		fmt.Fprintf(os.Stderr, "[request-ui] UI on http://%s/\n", ln.Addr())
	})
}

const schema = `
CREATE TABLE IF NOT EXISTS requests (
	id                    BIGSERIAL PRIMARY KEY,
	ts                    TIMESTAMPTZ NOT NULL DEFAULT now(),
	request_id            TEXT,
	method                TEXT,
	path                  TEXT,
	host                  TEXT,
	trace_id              TEXT,
	span_id               TEXT,
	request_headers       TEXT,
	request_body          TEXT,
	upstream_status       TEXT,
	upstream_address      TEXT,
	response_headers      TEXT,
	response_body         TEXT,
	error_details         TEXT,
	response_flags        TEXT,
	response_code_details TEXT,
	upstream_failure      TEXT,
	duration_ms           DOUBLE PRECISION,
	request_size_bytes    DOUBLE PRECISION,
	response_size_bytes   DOUBLE PRECISION,
	response_code         DOUBLE PRECISION,
	has_error             BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS requests_ts     ON requests (ts DESC);
CREATE INDEX IF NOT EXISTS requests_error  ON requests (has_error) WHERE has_error;
`

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schema)
	return err
}

// writer drains the channel and batch-inserts into Postgres.
// Flushes every 100ms or when 100 records accumulate.
func (s *Sink) writer() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	batch := make([]*Record, 0, 100)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		rows := make([][]any, len(batch))
		for i, r := range batch {
			rows[i] = []any{
				r.RequestID, r.Method, r.Path, r.Host, r.TraceID, r.SpanID,
				r.RequestHeaders, r.RequestBody,
				r.UpstreamStatus, r.UpstreamAddress,
				r.ResponseHeaders, r.ResponseBody,
				r.ErrorDetails, r.ResponseFlags, r.ResponseCodeDetails, r.UpstreamFailure,
				r.DurationMs, r.RequestSizeBytes, r.ResponseSizeBytes, r.ResponseCode,
				r.HasError,
			}
		}

		// Use pgx COPY for bulk insert -- much faster than individual INSERTs.
		n, err := s.pool.CopyFrom(ctx,
			pgx.Identifier{"requests"},
			[]string{
				"request_id", "method", "path", "host", "trace_id", "span_id",
				"request_headers", "request_body",
				"upstream_status", "upstream_address",
				"response_headers", "response_body",
				"error_details", "response_flags", "response_code_details", "upstream_failure",
				"duration_ms", "request_size_bytes", "response_size_bytes", "response_code",
				"has_error",
			},
			pgx.CopyFromRows(rows),
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[request-ui] copy: %v\n", err)
			batch = batch[:0]
			return
		}
		if n > 0 {
			// Fetch the just-inserted records with their DB-assigned IDs for SSE.
			// We use a short window -- the last N rows -- rather than per-batch IDs.
			s.broadcastLatest(ctx, int(n))
		}
		batch = batch[:0]
	}

	for {
		select {
		case r := <-s.ch:
			batch = append(batch, r)
			if len(batch) >= 100 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// broadcastLatest fetches the most recently inserted N records and pushes them
// to SSE subscribers.
func (s *Sink) broadcastLatest(ctx context.Context, n int) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+selectCols+` FROM requests ORDER BY id DESC LIMIT $1`, n)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRecord(rows)
		if err == nil {
			s.bcast.send(r)
		}
	}
}

const selectCols = `id, to_char(ts AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	request_id, method, path, host, trace_id, span_id,
	request_headers, request_body, upstream_status, upstream_address,
	response_headers, response_body, error_details, response_flags,
	response_code_details, upstream_failure,
	duration_ms, request_size_bytes, response_size_bytes, response_code, has_error`

func scanRecord(rows interface {
	Scan(dest ...any) error
}) (*Record, error) {
	r := &Record{}
	return r, rows.Scan(
		&r.ID, &r.Timestamp,
		&r.RequestID, &r.Method, &r.Path, &r.Host, &r.TraceID, &r.SpanID,
		&r.RequestHeaders, &r.RequestBody,
		&r.UpstreamStatus, &r.UpstreamAddress,
		&r.ResponseHeaders, &r.ResponseBody,
		&r.ErrorDetails, &r.ResponseFlags, &r.ResponseCodeDetails, &r.UpstreamFailure,
		&r.DurationMs, &r.RequestSizeBytes, &r.ResponseSizeBytes, &r.ResponseCode,
		&r.HasError,
	)
}

// handleRequests serves GET /api/requests.
//
// Query params:
//
//	since=ID   records with id > ID (ascending, for polling)
//	q=TEXT     iLIKE search on method, path, flags, error fields
//	errors=1   only records where has_error = true
//	limit=N    max rows (default 500, max 5000)
func (s *Sink) handleRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := 500
	if l := q.Get("limit"); l != "" {
		if n, _ := strconv.Atoi(l); n > 0 && n <= 5000 {
			limit = n
		}
	}

	var (
		args  []any
		where string
	)

	switch {
	case q.Get("q") != "":
		pat := "%" + q.Get("q") + "%"
		where = `WHERE (method ILIKE $1 OR path ILIKE $1 OR response_flags ILIKE $1
			OR error_details ILIKE $1 OR response_code_details ILIKE $1
			OR upstream_failure ILIKE $1 OR request_id ILIKE $1)`
		args = []any{pat, limit}
	case q.Get("since") != "":
		where = `WHERE id > $1`
		args = []any{q.Get("since"), limit}
	case q.Get("errors") == "1":
		where = `WHERE has_error = true`
		args = []any{limit}
	default:
		args = []any{limit}
	}

	// Determine ORDER direction: since= wants ASC, everything else DESC.
	order := "DESC"
	if q.Get("since") != "" {
		order = "ASC"
	}

	limitIdx := len(args) // $N for LIMIT
	query := fmt.Sprintf(`SELECT %s FROM requests %s ORDER BY id %s LIMIT $%d`,
		selectCols, where, order, limitIdx)

	rows, err := s.pool.Query(r.Context(), query, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	records := make([]*Record, 0, 64)
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err == nil {
			records = append(records, rec)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(records) //nolint:errcheck
}

// handleStream serves GET /api/stream as an SSE endpoint.
func (s *Sink) handleStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch := s.bcast.subscribe()
	defer s.bcast.unsubscribe(ch)

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case rec, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(rec)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// broadcaster fans out records to SSE subscribers.
type broadcaster struct {
	mu   sync.Mutex
	subs map[chan *Record]struct{}
}

func newBroadcaster() *broadcaster {
	return &broadcaster{subs: make(map[chan *Record]struct{})}
}

func (b *broadcaster) subscribe() chan *Record {
	ch := make(chan *Record, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *broadcaster) unsubscribe(ch chan *Record) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

func (b *broadcaster) send(r *Record) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- r:
		default:
		}
	}
}
