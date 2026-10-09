package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emelvv/tvpoll/internal/config"
	"github.com/emelvv/tvpoll/internal/ingest"
	"github.com/emelvv/tvpoll/internal/model"
)

type Store interface {
	CreatePoll(context.Context, model.Poll, string) (model.Poll, bool, error)
	GetPoll(context.Context, string) (model.Poll, error)
	ListPolls(context.Context) ([]model.Poll, error)
	Results(context.Context, string, int) (model.Counts, error)
	Ping(context.Context) error
}
type Voter interface {
	Enqueue(model.Ballot) (<-chan ingest.Result, error)
}
type API struct {
	store     Store
	voter     Voter
	cfg       config.Config
	identity  identity
	admission chan struct{}
	cacheMu   sync.RWMutex
	cache     map[string]model.Poll
	order     []string
	loadMu    sync.Mutex
	busy      atomic.Uint64
	metrics   *ingest.Metrics
}

func New(s Store, v Voter, c config.Config, assets fs.FS, m *ingest.Metrics) http.Handler {
	a := &API{store: s, voter: v, cfg: c, identity: identity{[]byte(c.CookieSecret), []byte(c.DedupSecret), c.CookieSecure}, admission: make(chan struct{}, c.MaxInFlight), cache: map[string]model.Poll{}, metrics: m}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", a.ready)
	mux.HandleFunc("POST /api/session", a.session)
	mux.HandleFunc("GET /api/polls/{id}", a.poll)
	mux.HandleFunc("POST /api/polls/{id}/votes", a.vote)
	mux.HandleFunc("POST /api/admin/polls", a.admin(a.create))
	mux.HandleFunc("GET /api/admin/polls", a.admin(a.list))
	mux.HandleFunc("GET /api/admin/polls/{id}/results", a.admin(a.results))
	mux.HandleFunc("GET /metrics", a.admin(a.exposeMetrics))
	if assets != nil {
		files := http.FileServer(http.FS(assets))
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/admin" {
				r.URL.Path = "/admin.html"
			}
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
		})
	}
	return a.middleware(mux)
}

func (a *API) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodPost && r.Header.Get("Origin") != "" && r.Header.Get("Origin") != a.cfg.PublicOrigin {
			fail(w, 403, "cross_origin", "request origin is not allowed")
			return
		}
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		select {
		case a.admission <- struct{}{}:
			defer func() { <-a.admission }()
			next.ServeHTTP(w, r)
		default:
			a.busy.Add(1)
			w.Header().Set("Retry-After", "1")
			fail(w, 503, "overloaded", "retry with the same browser identity")
		}
	})
}

func (a *API) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := sha256.Sum256([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
		expected := sha256.Sum256([]byte(a.cfg.AdminToken))
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, 401, "unauthorized", "admin token required")
			return
		}
		h(w, r)
	}
}

func (a *API) session(w http.ResponseWriter, r *http.Request) {
	if _, err := a.identity.read(r, time.Now()); err != nil {
		if err = a.identity.issue(w, time.Now()); err != nil {
			fail(w, 503, "unavailable", "identity unavailable")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.DBTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		fail(w, 503, "not_ready", "database unavailable")
		return
	}
	respond(w, 200, map[string]string{"status": "ready"})
}

func (a *API) getPoll(ctx context.Context, id string) (model.Poll, error) {
	a.cacheMu.RLock()
	p, ok := a.cache[id]
	a.cacheMu.RUnlock()
	if ok {
		return p, nil
	}
	// Serialize cache fills. Immutability makes cache invalidation unnecessary.
	a.loadMu.Lock()
	defer a.loadMu.Unlock()
	a.cacheMu.RLock()
	p, ok = a.cache[id]
	a.cacheMu.RUnlock()
	if ok {
		return p, nil
	}
	p, err := a.store.GetPoll(ctx, id)
	if err != nil {
		return p, err
	}
	a.cachePoll(p)
	return p, nil
}

func (a *API) cachePoll(p model.Poll) {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if _, ok := a.cache[p.ID]; ok {
		return
	}
	if len(a.order) >= 1024 {
		delete(a.cache, a.order[0])
		a.order = a.order[1:]
	}
	a.cache[p.ID] = p
	a.order = append(a.order, p.ID)
}

func (a *API) lookup(w http.ResponseWriter, r *http.Request) (model.Poll, bool) {
	id := r.PathValue("id")
	if !validID(id) {
		fail(w, 404, "not_found", "poll not found")
		return model.Poll{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.DBTimeout)
	defer cancel()
	p, err := a.getPoll(ctx, id)
	if errors.Is(err, model.ErrNotFound) {
		fail(w, 404, "not_found", "poll not found")
		return p, false
	}
	if err != nil {
		fail(w, 503, "unavailable", "poll unavailable")
		return p, false
	}
	return p, true
}

func (a *API) poll(w http.ResponseWriter, r *http.Request) {
	p, ok := a.lookup(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60, immutable")
	w.Header().Set("ETag", `"`+p.ID+`"`)
	if r.Header.Get("If-None-Match") == `"`+p.ID+`"` {
		w.WriteHeader(304)
		return
	}
	respond(w, 200, p)
}

func (a *API) vote(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	voter, err := a.identity.read(r, now)
	if err != nil {
		fail(w, 403, "identity_required", "enable cookies and establish a session before voting")
		return
	}
	var request struct {
		Choices []int `json:"choices"`
	}
	if err := decode(w, r, &request); err != nil {
		fail(w, 400, "invalid_request", err.Error())
		return
	}
	p, ok := a.lookup(w, r)
	if !ok {
		return
	}
	// Admission time is checked after metadata lookup and body validation.
	now = time.Now()
	if now.Before(p.OpensAt) || !now.Before(p.ClosesAt) {
		fail(w, 409, "poll_closed", "poll is not accepting votes")
		return
	}
	mask, err := choiceMask(p, request.Choices)
	if err != nil {
		fail(w, 400, "invalid_choices", err.Error())
		return
	}
	result, err := a.voter.Enqueue(model.Ballot{PollID: p.ID, Digest: a.identity.digest(p.ID, voter), Mask: mask, ReceivedAt: now})
	if err != nil {
		w.Header().Set("Retry-After", "1")
		fail(w, 503, "overloaded", "retry with the same browser identity")
		return
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case res := <-result:
		if res.Err != nil {
			w.Header().Set("Retry-After", "1")
			fail(w, 503, "unavailable", "outcome may be unknown; retry with the same browser identity")
			return
		}
		if res.Accepted {
			respond(w, 201, map[string]string{"status": "accepted"})
		} else {
			respond(w, 200, map[string]string{"status": "already_voted"})
		}
	case <-r.Context().Done():
		// Storage is independent of request cancellation after queue admission.
		return
	case <-timer.C:
		w.Header().Set("Retry-After", "1")
		fail(w, 503, "outcome_unknown", "confirmation timed out; retry with the same browser identity")
	}
}

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 || strings.TrimSpace(key) != key {
		fail(w, 400, "invalid_key", "provide an Idempotency-Key of 8..128 characters")
		return
	}
	var p model.Poll
	if err := decode(w, r, &p); err != nil {
		fail(w, 400, "invalid_request", err.Error())
		return
	}
	if err := validatePoll(&p, time.Now()); err != nil {
		fail(w, 400, "invalid_poll", err.Error())
		return
	}
	id, err := uuid()
	if err != nil {
		fail(w, 503, "unavailable", "id generator unavailable")
		return
	}
	p.ID = id
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.DBTimeout)
	defer cancel()
	p, replay, err := a.store.CreatePoll(ctx, p, key)
	if errors.Is(err, model.ErrConflict) {
		fail(w, 409, "idempotency_conflict", "key already used for another poll")
		return
	}
	if err != nil {
		fail(w, 503, "unavailable", "creation outcome may be unknown; retry with the same key and payload")
		return
	}
	a.cachePoll(p)
	status := 201
	if replay {
		status = 200
	}
	respond(w, status, p)
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.DBTimeout)
	defer cancel()
	polls, err := a.store.ListPolls(ctx)
	if err != nil {
		fail(w, 503, "unavailable", "poll list unavailable")
		return
	}
	respond(w, 200, map[string]any{"polls": polls})
}

func (a *API) results(w http.ResponseWriter, r *http.Request) {
	p, ok := a.lookup(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.DBTimeout)
	defer cancel()
	counts, err := a.store.Results(ctx, p.ID, len(p.Options))
	if err != nil {
		fail(w, 503, "incomplete_results", "one or more shards unavailable; retry later")
		return
	}
	respond(w, 200, map[string]any{"poll": p, "total_votes": counts.Total, "option_counts": counts.Options, "observed_at": time.Now().UTC(), "consistent": false})
}

func (a *API) exposeMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# TYPE efir_http_admission_rejections_total counter\nefir_http_admission_rejections_total %d\n", a.busy.Load())
	if a.metrics != nil {
		m := a.metrics
		fmt.Fprintf(w, "# TYPE efir_votes_accepted_total counter\nefir_votes_accepted_total %d\n# TYPE efir_votes_duplicate_total counter\nefir_votes_duplicate_total %d\n# TYPE efir_votes_rejected_total counter\nefir_votes_rejected_total %d\n# TYPE efir_votes_errors_total counter\nefir_votes_errors_total %d\n# TYPE efir_batches_total counter\nefir_batches_total %d\n# TYPE efir_votes_pending gauge\nefir_votes_pending %d\n", m.Accepted.Load(), m.Duplicate.Load(), m.Rejected.Load(), m.Errors.Load(), m.Batches.Load(), m.Queued.Load())
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("invalid JSON, unknown field, or body exceeds 16 KiB")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("body must contain exactly one JSON object")
	}
	return nil
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code, msg string) {
	respond(w, status, map[string]string{"error": code, "message": msg})
}
