// Package demo provisions a real daily poll for an explicitly enabled public
// demonstration. Background provisioning is separate from read-only redirects.
package demo

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/emelvv/tvpoll/internal/model"
)

type Store interface {
	CreatePoll(context.Context, model.Poll, string) (model.Poll, bool, error)
}

// Service owns the optional background seeder and its current ready poll.
// Close must be called before closing its Store.
type Service struct {
	store   Store
	timeout time.Duration
	now     func() time.Time
	mu      sync.RWMutex
	poll    model.Poll
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// New starts provisioning asynchronously, then retries once per minute. Requests
// never create polls. An unavailable initial seed does not prevent API startup.
func New(store Store, timeout time.Duration) *Service {
	if timeout <= 0 || timeout > 2*time.Second {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{store: store, timeout: timeout, now: time.Now, cancel: cancel}
	s.wg.Go(func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			if err := s.seed(ctx, s.now()); err != nil && ctx.Err() == nil {
				// Database errors may contain connection details. Keep logs generic.
				slog.Warn("demo poll unavailable; retrying in background")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
	return s
}

// Close cancels an in-flight seed and waits for the background worker. Calling
// Close repeatedly is safe, including while another caller is closing it.
func (s *Service) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

func (s *Service) seed(parent context.Context, now time.Time) error {
	if err := parent.Err(); err != nil {
		return err
	}
	p, key := dailyPoll(now)
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	defer cancel()
	created, _, err := s.store.CreatePoll(ctx, p, key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.poll = created
	s.mu.Unlock()
	return nil
}

func dailyPoll(now time.Time) (model.Poll, string) {
	u := now.UTC()
	start := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	key := "efir-demo-v1:" + start.Format("2006-01-02")
	id := sha256.Sum256([]byte(key))
	// A deterministic custom UUIDv8: every process uses the same daily ID.
	id[6] = (id[6] & 0x0f) | 0x80
	id[8] = (id[8] & 0x3f) | 0x80
	p := model.Poll{
		ID:       fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:16]),
		Question: "Демонстрационный опрос: какой вид транспорта вы выбираете чаще?",
		Type:     "single", Options: []string{"Общественный транспорт", "Автомобиль", "Велосипед", "Пешком"},
		MinChoices: 1, MaxChoices: 1, OpensAt: start, ClosesAt: start.Add(24 * time.Hour),
	}
	return p, key
}

// Wrap adds only read-only demo entry points. Explicit poll links, the admin
// page and the API retain the underlying handler's behavior and protections.
func (s *Service) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/" && !r.URL.Query().Has("poll") {
			demoHeaders(w)
			http.Redirect(w, r, "/demo", http.StatusFound)
			return
		}
		if r.URL.Path != "/demo" {
			next.ServeHTTP(w, r)
			return
		}
		demoHeaders(w)
		s.mu.RLock()
		p := s.poll
		s.mu.RUnlock()
		now := s.now()
		if p.ID == "" || now.Before(p.OpensAt) || !now.Before(p.ClosesAt) {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "Демонстрационный опрос готовится. Повторите попытку через несколько секунд.", http.StatusServiceUnavailable)
			return
		}
		http.Redirect(w, r, "/?poll="+url.QueryEscape(p.ID), http.StatusFound)
	})
}

func demoHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}
