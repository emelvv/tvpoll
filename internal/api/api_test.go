package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emelvv/tvpoll/internal/config"
	"github.com/emelvv/tvpoll/internal/ingest"
	"github.com/emelvv/tvpoll/internal/model"
)

const testID = "12345678-1234-4234-8234-123456789abc"

type fakeStore struct {
	p     model.Poll
	count int
	err   error
}

func (f *fakeStore) GetPoll(context.Context, string) (model.Poll, error) { return f.p, f.err }
func (f *fakeStore) CreatePoll(_ context.Context, p model.Poll, _ string) (model.Poll, bool, error) {
	f.count++
	return p, false, f.err
}
func (f *fakeStore) ListPolls(context.Context) ([]model.Poll, error) { return []model.Poll{f.p}, f.err }
func (f *fakeStore) Results(context.Context, string, int) (model.Counts, error) {
	return model.Counts{Total: 1, Options: []int64{1, 0}}, f.err
}
func (f *fakeStore) Ping(context.Context) error { return f.err }

type fakeVoter struct {
	mu      sync.Mutex
	ballots []model.Ballot
	seen    map[string]bool
	err     error
}

func (f *fakeVoter) Enqueue(b model.Ballot) (<-chan ingest.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	key := string(b.Digest)
	accepted := !f.seen[key]
	f.seen[key] = true
	f.ballots = append(f.ballots, b)
	ch := make(chan ingest.Result, 1)
	ch <- ingest.Result{Accepted: accepted}
	return ch, nil
}
func setup() (http.Handler, *fakeStore, *fakeVoter) {
	s := &fakeStore{p: model.Poll{ID: testID, Question: "Выбор", Type: "single", Options: []string{"А", "Б"}, MinChoices: 1, MaxChoices: 1, OpensAt: time.Now().Add(-time.Hour), ClosesAt: time.Now().Add(time.Hour)}}
	v := &fakeVoter{}
	c := config.Config{AdminToken: strings.Repeat("a", 32), CookieSecret: strings.Repeat("b", 32), DedupSecret: strings.Repeat("c", 32), PublicOrigin: "http://localhost:8080", MaxInFlight: 128, DBTimeout: time.Second}
	return New(s, v, c, nil, nil), s, v
}
func call(h http.Handler, method, path, body string, cookie *http.Cookie, admin bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if admin {
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
		r.Header.Set("Idempotency-Key", "test-key-0001")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func sessionCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	w := call(h, "POST", "/api/session", "", nil, false)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	return w.Result().Cookies()[0]
}

func TestAnonymousVoteRetryAndPrivacy(t *testing.T) {
	h, _, v := setup()
	c := sessionCookie(t, h)
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.MaxAge <= 0 {
		t.Fatalf("unsafe cookie: %+v", c)
	}
	for n, want := range []int{201, 200} {
		w := call(h, "POST", "/api/polls/"+testID+"/votes", `{"choices":[0]}`, c, false)
		if w.Code != want {
			t.Fatalf("attempt %d: %d %s", n, w.Code, w.Body)
		}
	}
	if len(v.ballots) != 2 || !bytes.Equal(v.ballots[0].Digest, v.ballots[1].Digest) || len(v.ballots[0].Digest) != 32 {
		t.Fatal("retry identity changed")
	}
	w := call(h, "GET", "/api/polls/"+testID, "", nil, false)
	if len(w.Result().Cookies()) != 0 || !strings.Contains(w.Header().Get("Cache-Control"), "public") {
		t.Fatal("metadata should be CDN cacheable without identity")
	}
	if strings.Contains(w.Body.String(), "voter") || strings.Contains(w.Body.String(), "digest") {
		t.Fatal("identity exposed")
	}
}

func TestRejectedRequestsDoNotEnqueue(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		cookie     bool
		want       int
	}{
		{"missing cookie", `{"choices":[0]}`, false, 403}, {"empty", `{"choices":[]}`, true, 400}, {"range", `{"choices":[2]}`, true, 400},
		{"duplicate", `{"choices":[0,0]}`, true, 400}, {"unknown field", `{"choices":[0],"ip":"spoof"}`, true, 400}, {"trailing JSON", `{"choices":[0]} {}`, true, 400},
		{"oversized", `{"choices":[0],"padding":"` + strings.Repeat("x", 17000) + `"}`, true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, v := setup()
			var c *http.Cookie
			if tc.cookie {
				c = sessionCookie(t, h)
			}
			w := call(h, "POST", "/api/polls/"+testID+"/votes", tc.body, c, false)
			if w.Code != tc.want || len(v.ballots) != 0 {
				t.Fatal(w.Code, w.Body, len(v.ballots))
			}
		})
	}
}

func TestPollWindowAndOverload(t *testing.T) {
	for _, future := range []bool{false, true} {
		h, s, v := setup()
		if future {
			s.p.OpensAt = time.Now().Add(time.Hour)
		} else {
			s.p.ClosesAt = time.Now().Add(-time.Second)
		}
		w := call(h, "POST", "/api/polls/"+testID+"/votes", `{"choices":[0]}`, sessionCookie(t, h), false)
		if w.Code != 409 || len(v.ballots) != 0 {
			t.Fatal(w.Code, w.Body)
		}
	}
	h, _, v := setup()
	v.err = ingest.ErrBusy
	w := call(h, "POST", "/api/polls/"+testID+"/votes", `{"choices":[0]}`, sessionCookie(t, h), false)
	if w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Fatal(w.Code, w.Body)
	}
}

func TestAdminBoundaryAndResultsUnavailable(t *testing.T) {
	h, s, _ := setup()
	w := call(h, "GET", "/api/admin/polls/"+testID+"/results", "", nil, false)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	w = call(h, "GET", "/api/admin/polls/"+testID+"/results", "", nil, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "digest") || strings.Contains(w.Body.String(), "cookie") {
		t.Fatal("identities in results")
	}
	s.err = errors.New("shard offline")
	w = call(h, "GET", "/api/admin/polls/"+testID+"/results", "", nil, true)
	if w.Code != 503 || strings.Contains(w.Body.String(), "total_votes") {
		t.Fatal("partial results represented as complete")
	}
}

func TestCreateValidationAndOrigin(t *testing.T) {
	h, s, _ := setup()
	p := s.p
	p.ID = ""
	body, _ := json.Marshal(p)
	w := call(h, "POST", "/api/admin/polls", string(body), nil, true)
	if w.Code != 201 || s.count != 1 {
		t.Fatal(w.Code, w.Body)
	}
	r := httptest.NewRequest("POST", "/api/session", nil)
	r.Header.Set("Origin", "https://attacker.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || len(w.Result().Cookies()) > 0 {
		t.Fatal("cross-origin mutation")
	}
	p.Options = []string{"А", " а "}
	body, _ = json.Marshal(p)
	w = call(h, "POST", "/api/admin/polls", string(body), nil, true)
	if w.Code != 400 || s.count != 1 {
		t.Fatal("invalid create reached persistence")
	}
}

func TestSignedCookieTamperExpiryAndPollScope(t *testing.T) {
	id := identity{signing: []byte(strings.Repeat("s", 32)), dedup: []byte(strings.Repeat("d", 32)), secure: true}
	now := time.Now()
	w := httptest.NewRecorder()
	if err := id.issue(w, now); err != nil {
		t.Fatal(err)
	}
	c := w.Result().Cookies()[0]
	r := httptest.NewRequest("POST", "/", nil)
	r.AddCookie(c)
	v, err := id.read(r, now)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(id.digest(testID, v), id.digest("other-poll", v)) {
		t.Fatal("cross poll linkable digest")
	}
	if _, err = id.read(r, now.Add(identityLifetime+time.Minute)); err == nil {
		t.Fatal("expired accepted")
	}
	changed := "A"
	if c.Value[30] == 'A' {
		changed = "B"
	}
	c.Value = c.Value[:30] + changed + c.Value[31:]
	r = httptest.NewRequest("POST", "/", nil)
	r.AddCookie(c)
	if _, err = id.read(r, now); err == nil {
		t.Fatal("tamper accepted")
	}
}

func TestChoiceMask32AndMultiple(t *testing.T) {
	p := model.Poll{Options: make([]string, 32), MinChoices: 1, MaxChoices: 32}
	mask, err := choiceMask(p, []int{0, 31})
	if err != nil || mask != 2147483649 {
		t.Fatal(mask, err)
	}
	if _, err = choiceMask(p, []int{1, 1}); err == nil {
		t.Fatal("duplicate choices accepted")
	}
	if validID(strings.ToUpper(testID)) || validID("../"+testID) {
		t.Fatal("noncanonical poll path accepted")
	}
}
