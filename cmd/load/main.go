// Command load is a closed-loop correctness smoke / benchmark client.
// It exercises the public session API: each simulated voter has its own cookie jar.
// This is not an open-loop model of a national TV traffic burst.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type config struct {
	Base           string
	Token          string
	Mode           string
	Voters         int
	Concurrency    int
	DuplicateEvery int
	Retries        int
	Duration       time.Duration
}

type summary struct {
	Mode                 string         `json:"mode"`
	TrafficModel         string         `json:"traffic_model"`
	PollID               string         `json:"poll_id"`
	VotingURL            string         `json:"voting_url"`
	Voters               int            `json:"voters"`
	Concurrency          int            `json:"concurrency"`
	Completed            int            `json:"completed_voters"`
	Accepted             int            `json:"accepted"`
	AlreadyVoted         int            `json:"already_voted"`
	DuplicatesVerified   int            `json:"duplicates_verified"`
	Retries              int            `json:"retries"`
	Failures             int            `json:"failures"`
	FailureSamples       []string       `json:"failure_samples,omitempty"`
	ObservedVotes        int64          `json:"observed_votes"`
	ExpectedOptionCounts []int64        `json:"expected_option_counts"`
	ObservedOptionCounts []int64        `json:"observed_option_counts"`
	CountsMatch          bool           `json:"counts_match"`
	ElapsedSeconds       float64        `json:"elapsed_seconds"`
	AcceptedPerSecond    float64        `json:"accepted_per_second"`
	VoteLatencyMS        latencySummary `json:"vote_latency_ms"`
}

type latencySummary struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

type outcome struct {
	Accepted     bool
	AlreadyVoted bool
	Duplicate    bool
	Retries      int
	Latency      time.Duration
	Choice       int
	Err          error
}

type poll struct {
	ID string `json:"id"`
}

type results struct {
	TotalVotes   int64   `json:"total_votes"`
	OptionCounts []int64 `json:"option_counts"`
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.Base, "base", "http://localhost:8080", "service URL; use the same hostname as PUBLIC_ORIGIN")
	flag.StringVar(&cfg.Token, "token", "", "admin token (prefer ADMIN_TOKEN environment variable)")
	flag.StringVar(&cfg.Mode, "mode", "smoke", "smoke or benchmark; both are closed-loop correctness runs")
	flag.IntVar(&cfg.Voters, "voters", 100, "number of unique browser sessions")
	flag.IntVar(&cfg.Concurrency, "concurrency", 16, "maximum concurrent voters")
	flag.IntVar(&cfg.DuplicateEvery, "duplicate-every", 5, "repeat every Nth voter with the same cookie; 0 disables")
	flag.IntVar(&cfg.Retries, "retries", 5, "maximum retries for overload and uncertain transport outcomes")
	flag.DurationVar(&cfg.Duration, "poll-duration", 5*time.Minute, "poll voting window")
	flag.Parse()
	if cfg.Token == "" {
		cfg.Token = os.Getenv("ADMIN_TOKEN")
	}
	if err := validate(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := run(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if report.Failures != 0 || !report.CountsMatch || report.Completed != report.Voters {
		os.Exit(1)
	}
}

func validate(cfg config) error {
	u, err := url.Parse(cfg.Base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("-base must be an HTTP(S) origin without credentials, path, query or fragment")
	}
	if len(cfg.Token) < 32 {
		return errors.New("set ADMIN_TOKEN to the server's admin token (at least 32 characters)")
	}
	if cfg.Mode != "smoke" && cfg.Mode != "benchmark" {
		return errors.New("-mode must be smoke or benchmark")
	}
	if cfg.Voters < 1 || cfg.Concurrency < 1 || cfg.Concurrency > 10000 || cfg.DuplicateEvery < 0 || cfg.Retries < 0 || cfg.Retries > 20 || cfg.Duration < time.Second {
		return errors.New("invalid voters/concurrency/duplicate-every/retries/poll-duration")
	}
	return nil
}

func run(ctx context.Context, cfg config) (summary, error) {
	cfg.Base = strings.TrimRight(cfg.Base, "/")
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: cfg.Concurrency*2 + 10, MaxIdleConnsPerHost: cfg.Concurrency*2 + 10, MaxConnsPerHost: cfg.Concurrency*2 + 10, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	defer transport.CloseIdleConnections()
	adminClient := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: noRedirect}
	created, err := createPoll(ctx, adminClient, cfg)
	if err != nil {
		return summary{}, err
	}
	report := summary{Mode: cfg.Mode, TrafficModel: "closed-loop; not a proof of 100M votes/min capacity", PollID: created.ID, VotingURL: cfg.Base + "/?poll=" + created.ID, Voters: cfg.Voters, Concurrency: cfg.Concurrency, ExpectedOptionCounts: make([]int64, 4)}
	jobs := make(chan int)
	outcomes := make(chan outcome, cfg.Concurrency)
	var workers sync.WaitGroup
	for range min(cfg.Voters, cfg.Concurrency) {
		workers.Go(func() {
			for index := range jobs {
				outcomes <- vote(ctx, cfg, transport, created.ID, index)
			}
		})
	}
	started := time.Now()
	go func() {
		defer close(jobs)
		for index := 0; index < cfg.Voters; index++ {
			select {
			case jobs <- index:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(outcomes) }()
	latencies := make([]time.Duration, 0, cfg.Voters)
	for result := range outcomes {
		report.Completed++
		report.Retries += result.Retries
		if result.Accepted {
			report.Accepted++
		}
		if result.AlreadyVoted {
			report.AlreadyVoted++
		}
		if result.Accepted || result.AlreadyVoted {
			report.ExpectedOptionCounts[result.Choice]++
		}
		if result.Duplicate {
			report.DuplicatesVerified++
		}
		if result.Latency > 0 {
			latencies = append(latencies, result.Latency)
		}
		if result.Err != nil {
			report.Failures++
			if len(report.FailureSamples) < 8 {
				report.FailureSamples = append(report.FailureSamples, result.Err.Error())
			}
		}
	}
	report.ElapsedSeconds = time.Since(started).Seconds()
	report.AcceptedPerSecond = float64(report.Accepted) / report.ElapsedSeconds
	report.VoteLatencyMS = summarizeLatency(latencies)
	if ctx.Err() != nil {
		return report, fmt.Errorf("run interrupted: %w", ctx.Err())
	}
	var observed results
	code, _, err := request(ctx, adminClient, cfg.Base, http.MethodGet, "/api/admin/polls/"+created.ID+"/results", cfg.Token, "", nil, &observed)
	if err != nil {
		return report, fmt.Errorf("read final results: %w", err)
	}
	if code != http.StatusOK {
		return report, fmt.Errorf("read final results: HTTP %d", code)
	}
	report.ObservedVotes = observed.TotalVotes
	report.ObservedOptionCounts = observed.OptionCounts
	// A first vote that times out after commit can return 200 on retry. It is
	// nevertheless one successfully persisted unique voter in this fresh poll.
	report.CountsMatch = observed.TotalVotes == int64(report.Accepted+report.AlreadyVoted)
	if len(observed.OptionCounts) != len(report.ExpectedOptionCounts) {
		report.CountsMatch = false
	} else {
		for index, expected := range report.ExpectedOptionCounts {
			if observed.OptionCounts[index] != expected {
				report.CountsMatch = false
			}
		}
	}
	return report, nil
}

func noRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func createPoll(ctx context.Context, client *http.Client, cfg config) (poll, error) {
	now := time.Now().UTC()
	body := map[string]any{"question": "Какой вид транспорта вы выбираете чаще?", "type": "single", "options": []string{"Общественный транспорт", "Автомобиль", "Велосипед", "Пешком"}, "min_choices": 1, "max_choices": 1, "opens_at": now.Add(-time.Second).Format(time.RFC3339Nano), "closes_at": now.Add(cfg.Duration).Format(time.RFC3339Nano)}
	key, err := uuid()
	if err != nil {
		return poll{}, err
	}
	var created poll
	for attempt := 0; ; attempt++ {
		code, retryAfter, err := request(ctx, client, cfg.Base, http.MethodPost, "/api/admin/polls", cfg.Token, key, body, &created)
		if err == nil && (code == http.StatusCreated || code == http.StatusOK) && created.ID != "" {
			break
		}
		if (err == nil && code != http.StatusServiceUnavailable && code != http.StatusTooManyRequests) || attempt >= cfg.Retries || ctx.Err() != nil {
			if err != nil {
				return poll{}, fmt.Errorf("create poll: %w", err)
			}
			return poll{}, fmt.Errorf("create poll: HTTP %d", code)
		}
		if err := retryWait(ctx, attempt, retryAfter); err != nil {
			return poll{}, err
		}
	}
	// Exercise create idempotency and ensure a replay returns the same poll.
	var replay poll
	code, _, err := request(ctx, client, cfg.Base, http.MethodPost, "/api/admin/polls", cfg.Token, key, body, &replay)
	if err != nil || code != http.StatusOK || replay.ID != created.ID {
		return poll{}, fmt.Errorf("create idempotency check failed (HTTP %d)", code)
	}
	return created, nil
}

func vote(ctx context.Context, cfg config, transport *http.Transport, pollID string, index int) outcome {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return outcome{Err: err}
	}
	client := &http.Client{Transport: transport, Jar: jar, Timeout: 20 * time.Second, CheckRedirect: noRedirect}
	code, _, err := request(ctx, client, cfg.Base, http.MethodGet, "/api/polls/"+pollID, "", "", nil, nil)
	if err != nil {
		return outcome{Err: fmt.Errorf("load poll: %w", err)}
	}
	if code != http.StatusOK {
		return outcome{Err: fmt.Errorf("load poll: HTTP %d", code)}
	}
	code, _, err = request(ctx, client, cfg.Base, http.MethodPost, "/api/session", "", "", nil, nil)
	if err != nil {
		return outcome{Err: fmt.Errorf("create voter session: %w", err)}
	}
	if code != http.StatusNoContent {
		return outcome{Err: fmt.Errorf("create voter session: HTTP %d", code)}
	}
	origin, _ := url.Parse(cfg.Base)
	if len(jar.Cookies(origin)) == 0 {
		return outcome{Err: errors.New("session response did not create a usable cookie; check COOKIE_SECURE and the URL")}
	}
	started := time.Now()
	code, retries, err := submit(ctx, client, cfg, pollID, index%4)
	result := outcome{Accepted: code == http.StatusCreated, AlreadyVoted: code == http.StatusOK, Retries: retries, Latency: time.Since(started), Choice: index % 4, Err: err}
	if err != nil {
		return result
	}
	if cfg.DuplicateEvery > 0 && (index+1)%cfg.DuplicateEvery == 0 {
		code, retries, err = submit(ctx, client, cfg, pollID, (index+1)%4)
		result.Retries += retries
		if err != nil {
			result.Err = fmt.Errorf("duplicate request: %w", err)
		} else if code != http.StatusOK {
			result.Err = fmt.Errorf("duplicate request accepted again: HTTP %d", code)
		} else {
			result.Duplicate = true
		}
	}
	return result
}

func submit(ctx context.Context, client *http.Client, cfg config, pollID string, choice int) (int, int, error) {
	for attempt := 0; ; attempt++ {
		var value struct {
			Status string `json:"status"`
		}
		code, retryAfter, err := request(ctx, client, cfg.Base, http.MethodPost, "/api/polls/"+pollID+"/votes", "", "", map[string]any{"choices": []int{choice}}, &value)
		if err == nil && (code == http.StatusCreated || code == http.StatusOK) {
			if (code == http.StatusCreated && value.Status != "accepted") || (code == http.StatusOK && value.Status != "already_voted") {
				return code, attempt, fmt.Errorf("unexpected vote response: HTTP %d status %q", code, value.Status)
			}
			return code, attempt, nil
		}
		if (err == nil && code != http.StatusServiceUnavailable && code != http.StatusTooManyRequests) || attempt >= cfg.Retries || ctx.Err() != nil {
			if err != nil {
				return code, attempt, fmt.Errorf("submit vote: %w", err)
			}
			return code, attempt, fmt.Errorf("submit vote: HTTP %d", code)
		}
		if err := retryWait(ctx, attempt, retryAfter); err != nil {
			return code, attempt, err
		}
	}
}

func retryWait(ctx context.Context, attempt int, retryAfter time.Duration) error {
	wait := min(time.Duration(1<<min(attempt, 4))*100*time.Millisecond, 2*time.Second)
	if retryAfter > 0 {
		wait = min(retryAfter, 10*time.Second)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func request(ctx context.Context, client *http.Client, base, method, path, token, key string, body, target any) (int, time.Duration, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", base)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	seconds, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
	if target != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(target); err != nil {
			return resp.StatusCode, time.Duration(seconds) * time.Second, fmt.Errorf("decode response: %w", err)
		}
	}
	// Drain bounded small responses for connection reuse. No response payload or
	// Authorization value is printed, including on an error.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, time.Duration(seconds) * time.Second, nil
}

func uuid() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func summarizeLatency(values []time.Duration) latencySummary {
	if len(values) == 0 {
		return latencySummary{}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	percentile := func(percent int) float64 {
		index := min(len(values)-1, (len(values)*percent+99)/100-1)
		return float64(values[index]) / float64(time.Millisecond)
	}
	return latencySummary{P50: percentile(50), P95: percentile(95), P99: percentile(99), Max: float64(values[len(values)-1]) / float64(time.Millisecond)}
}
