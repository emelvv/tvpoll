package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests exercise the client over real HTTP. The fake server deliberately
// injects overload / a lost acknowledgement after persistence, then verifies
// that retries and a duplicate with a changed choice use the original cookie.
func TestRunRetriesPreserveIdentityAndOriginalChoice(t *testing.T) {
	for _, fault := range []string{"overload", "lost-acknowledgement"} {
		t.Run(fault, func(t *testing.T) {
			server, state := fakeAPI(t, fault, false)
			defer server.Close()
			cfg := config{Base: server.URL, Token: strings.Repeat("a", 32), Mode: "smoke", Voters: 12, Concurrency: 4, DuplicateEvery: 2, Retries: 2, Duration: time.Minute}
			report, err := run(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if report.Completed != 12 || report.Failures != 0 || !report.CountsMatch || report.ObservedVotes != 12 || report.DuplicatesVerified != 6 || report.Retries != 12 {
				t.Fatalf("unexpected report: %+v", report)
			}
			if fault == "overload" && (report.Accepted != 12 || report.AlreadyVoted != 0) {
				t.Fatalf("expected acceptance after overload: %+v", report)
			}
			if fault == "lost-acknowledgement" && (report.Accepted != 0 || report.AlreadyVoted != 12) {
				t.Fatalf("expected already-voted after lost ack: %+v", report)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.sessions != 12 || len(state.votes) != 12 || state.createCalls < 2 {
				t.Fatalf("unexpected server state: sessions=%d votes=%d creates=%d", state.sessions, len(state.votes), state.createCalls)
			}
		})
	}
}

func TestRunDetectsIncorrectOptionCounts(t *testing.T) {
	server, _ := fakeAPI(t, "", true)
	defer server.Close()
	report, err := run(context.Background(), config{Base: server.URL, Token: strings.Repeat("a", 32), Mode: "smoke", Voters: 4, Concurrency: 2, Duration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if report.Failures != 0 || report.ObservedVotes != 4 || report.CountsMatch {
		t.Fatalf("option-count corruption must fail correctness check: %+v", report)
	}
}

func TestCreateRetryReusesKeyAndBody(t *testing.T) {
	server, state := fakeAPI(t, "lost-create-acknowledgement", false)
	defer server.Close()
	created, err := createPoll(context.Background(), server.Client(), config{Base: server.URL, Token: strings.Repeat("a", 32), Retries: 2, Duration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "12345678-1234-4234-8234-123456789abc" {
		t.Fatalf("unexpected created poll: %+v", created)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.createCalls != 3 {
		t.Fatalf("expected initial attempt, retry, and explicit replay; got %d", state.createCalls)
	}
}

type apiState struct {
	mu          sync.Mutex
	sessions    int
	votes       map[string]int
	attempts    map[string]int
	createCalls int
	createKey   string
	createBody  string
}

func fakeAPI(t *testing.T, fault string, corruptCounts bool) (*httptest.Server, *apiState) {
	t.Helper()
	state := &apiState{votes: make(map[string]int), attempts: make(map[string]int)}
	const id = "12345678-1234-4234-8234-123456789abc"
	var origin string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.Header.Get("Origin") != origin {
			t.Errorf("incorrect Origin: %q", r.Header.Get("Origin"))
			http.Error(w, "origin", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/admin/") && r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 32) {
			t.Error("missing admin authorization")
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/admin/polls":
			body, _ := io.ReadAll(r.Body)
			key := r.Header.Get("Idempotency-Key")
			state.createCalls++
			if key == "" {
				t.Error("missing idempotency key")
			}
			if state.createCalls == 1 {
				state.createKey, state.createBody = key, string(body)
				w.WriteHeader(http.StatusCreated)
				if fault == "lost-create-acknowledgement" {
					_, _ = io.WriteString(w, "{")
					return
				}
			} else {
				if key != state.createKey || string(body) != state.createBody {
					t.Error("retry changed creation key or body")
					http.Error(w, "conflict", http.StatusConflict)
					return
				}
				w.WriteHeader(http.StatusOK)
			}
			_ = json.NewEncoder(w).Encode(poll{ID: id})
		case r.Method == http.MethodGet && r.URL.Path == "/api/polls/"+id:
			_ = json.NewEncoder(w).Encode(poll{ID: id})
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			state.sessions++
			http.SetCookie(w, &http.Cookie{Name: "voter", Value: fmt.Sprint(state.sessions), HttpOnly: true, Path: "/", SameSite: http.SameSiteLaxMode})
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/polls/"+id+"/votes":
			cookie, err := r.Cookie("voter")
			if err != nil {
				t.Error("vote has no cookie")
				http.Error(w, "session", http.StatusForbidden)
				return
			}
			state.attempts[cookie.Value]++
			if fault == "overload" && state.attempts[cookie.Value] == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if _, exists := state.votes[cookie.Value]; exists {
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "already_voted"})
				return
			}
			var body struct {
				Choices []int `json:"choices"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Choices) != 1 {
				t.Error("invalid vote body")
				http.Error(w, "body", http.StatusBadRequest)
				return
			}
			state.votes[cookie.Value] = body.Choices[0]
			w.WriteHeader(http.StatusCreated)
			if fault == "lost-acknowledgement" {
				_, _ = io.WriteString(w, "{")
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/admin/polls/"+id+"/results":
			counts := make([]int64, 4)
			for _, choice := range state.votes {
				counts[choice]++
			}
			if corruptCounts {
				counts[0]++
			}
			_ = json.NewEncoder(w).Encode(results{TotalVotes: int64(len(state.votes)), OptionCounts: counts})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	origin = server.URL
	return server, state
}
