package demo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emelvv/tvpoll/internal/model"
)

type memoryStore struct {
	mu     sync.Mutex
	polls  map[string]model.Poll
	bodies map[string]string
	calls  int
	err    error
}

func (s *memoryStore) CreatePoll(_ context.Context, p model.Poll, key string) (model.Poll, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return model.Poll{}, false, s.err
	}
	body, _ := json.Marshal(p)
	if previous, ok := s.polls[key]; ok {
		if s.bodies[key] != string(body) {
			return model.Poll{}, false, model.ErrConflict
		}
		return previous, true, nil
	}
	if s.polls == nil {
		s.polls = map[string]model.Poll{}
		s.bodies = map[string]string{}
	}
	s.polls[key], s.bodies[key] = p, string(body)
	return p, false, nil
}

func instant(t *testing.T, text string) time.Time {
	t.Helper()
	value, err := time.Parse(time.RFC3339, text)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDailyPollUsesUTCDayAndExactWindow(t *testing.T) {
	// 00:30 in Moscow is still the previous UTC day.
	p, key := dailyPoll(instant(t, "2026-10-09T00:30:00+03:00"))
	if key != "efir-demo-v1:2026-10-08" || !p.OpensAt.Equal(instant(t, "2026-10-08T00:00:00Z")) || !p.ClosesAt.Equal(instant(t, "2026-10-09T00:00:00Z")) || p.ClosesAt.Sub(p.OpensAt) != 24*time.Hour {
		t.Fatalf("incorrect daily window: %+v key=%q", p, key)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(p.ID) {
		t.Fatalf("invalid custom UUID: %q", p.ID)
	}
	if p.Type != "single" || p.MinChoices != 1 || p.MaxChoices != 1 || len(p.Options) != 4 || !strings.HasPrefix(p.Question, "Демонстрационный опрос:") {
		t.Fatalf("incorrect demo ballot: %+v", p)
	}
	same, sameKey := dailyPoll(instant(t, "2026-10-08T23:59:59Z"))
	if same.ID != p.ID || sameKey != key {
		t.Fatal("same UTC day changed poll identity")
	}
	next, nextKey := dailyPoll(p.ClosesAt)
	if next.ID == p.ID || nextKey == key || !next.OpensAt.Equal(p.ClosesAt) {
		t.Fatal("midnight did not advance the daily poll")
	}
}

func TestSeedRepeatsOnePollAndRollsOver(t *testing.T) {
	store := &memoryStore{}
	s := &Service{store: store, timeout: time.Second}
	day := instant(t, "2026-10-09T12:00:00Z")
	if err := s.seed(context.Background(), day); err != nil {
		t.Fatal(err)
	}
	first := s.poll
	if err := s.seed(context.Background(), day.Add(10*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s.poll.ID != first.ID || len(store.polls) != 1 || store.calls != 2 {
		t.Fatal("repeated seeding created another poll")
	}
	if err := s.seed(context.Background(), first.ClosesAt); err != nil {
		t.Fatal(err)
	}
	if s.poll.ID == first.ID || len(store.polls) != 2 || !s.poll.OpensAt.Equal(first.ClosesAt) || s.poll.ClosesAt.Sub(s.poll.OpensAt) != 24*time.Hour {
		t.Fatal("daily rollover failed")
	}
}

func TestSeedFailureRetainsPreviouslyReadyPoll(t *testing.T) {
	store := &memoryStore{}
	day := instant(t, "2026-10-09T12:00:00Z")
	s := &Service{store: store, timeout: time.Second, now: func() time.Time { return day }}
	if err := s.seed(context.Background(), day); err != nil {
		t.Fatal(err)
	}
	first := s.poll
	store.err = errors.New("database unavailable")
	if err := s.seed(context.Background(), day); err == nil {
		t.Fatal("seed failure was hidden")
	}
	if s.poll.ID != first.ID {
		t.Fatal("seed failure discarded the last ready poll")
	}
	w := httptest.NewRecorder()
	s.Wrap(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/demo", nil))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/?poll="+first.ID {
		t.Fatal("valid previous seed is no longer accessible")
	}
}

func TestRedirectsReadCacheWithoutProvisioning(t *testing.T) {
	day := instant(t, "2026-10-09T12:00:00Z")
	poll, _ := dailyPoll(day)
	store := &memoryStore{}
	s := &Service{store: store, poll: poll, now: func() time.Time { return day }}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := s.Wrap(next)
	for _, test := range []struct {
		method, path, location string
		code                   int
	}{
		{http.MethodGet, "/", "/demo", http.StatusFound},
		{http.MethodGet, "/?utm_source=example", "/demo", http.StatusFound},
		{http.MethodGet, "/demo", "/?poll=" + poll.ID, http.StatusFound},
		{http.MethodHead, "/demo", "/?poll=" + poll.ID, http.StatusFound},
		{http.MethodGet, "/?poll=" + poll.ID, "", http.StatusTeapot},
		{http.MethodGet, "/?poll=", "", http.StatusTeapot},
		{http.MethodGet, "/admin", "", http.StatusTeapot},
		{http.MethodGet, "/api/admin/polls", "", http.StatusTeapot},
		{http.MethodPost, "/demo", "", http.StatusTeapot},
		{http.MethodPost, "/", "", http.StatusTeapot},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
		if w.Code != test.code || w.Header().Get("Location") != test.location {
			t.Fatalf("%s %s: status=%d location=%q", test.method, test.path, w.Code, w.Header().Get("Location"))
		}
		if test.code == http.StatusFound && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("daily redirect can be cached past closing time")
		}
	}
	if store.calls != 0 {
		t.Fatal("public GET provisioned a poll")
	}
}

func TestUnavailableFutureAndExpiredPollsNeverRedirectToClosedBallot(t *testing.T) {
	day := instant(t, "2026-10-09T12:00:00Z")
	valid, _ := dailyPoll(day)
	future, _ := dailyPoll(day.Add(24 * time.Hour))
	for _, test := range []struct {
		name string
		poll model.Poll
		now  time.Time
	}{
		{"not-seeded", model.Poll{}, day},
		{"future", future, day},
		{"exact-close", valid, valid.ClosesAt},
		{"expired", valid, valid.ClosesAt.Add(time.Second)},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{}
			s := &Service{store: store, poll: test.poll, now: func() time.Time { return test.now }}
			w := httptest.NewRecorder()
			s.Wrap(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/demo", nil))
			if w.Code != http.StatusServiceUnavailable || w.Header().Get("Location") != "" || w.Header().Get("Retry-After") != "5" || !strings.Contains(w.Body.String(), "опрос готовится") {
				t.Fatalf("unavailable demo response: %d %s", w.Code, w.Body.String())
			}
			if store.calls != 0 {
				t.Fatal("unavailable public GET attempted a write")
			}
		})
	}
}

type blockingStore struct {
	entered   chan time.Duration
	cancelled chan struct{}
	release   chan struct{}
}

func (s *blockingStore) CreatePoll(ctx context.Context, _ model.Poll, _ string) (model.Poll, bool, error) {
	deadline, _ := ctx.Deadline()
	s.entered <- time.Until(deadline)
	<-ctx.Done()
	close(s.cancelled)
	<-s.release
	return model.Poll{}, false, ctx.Err()
}

func TestNewSeedsAsynchronouslyAndCloseWaitsForCancellation(t *testing.T) {
	store := &blockingStore{entered: make(chan time.Duration, 1), cancelled: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(store.release) })
	s := New(store, 30*time.Second)
	select {
	case deadline := <-store.entered:
		if deadline <= 0 || deadline > 2*time.Second {
			t.Fatalf("unbounded seed deadline: %s", deadline)
		}
	case <-time.After(time.Second):
		t.Fatal("background seed did not start")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-store.cancelled:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel pending seed")
	}
	select {
	case <-closed:
		t.Fatal("close returned before storage work exited")
	default:
	}
	release.Do(func() { close(store.release) })
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not join the worker")
	}
	s.Close() // A second close is safe.
}
