package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emelvv/tvpoll/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

type integrationFixture struct {
	store      *Store
	controlURL string
	shardURLs  []string
	pollIDs    []string
}

func integrationStore(t *testing.T) *integrationFixture {
	t.Helper()
	control := os.Getenv("TEST_CONTROL_DATABASE_URL")
	shards := os.Getenv("TEST_SHARD_DATABASE_URLS")
	if control == "" || shards == "" {
		t.Skip("set TEST_CONTROL_DATABASE_URL and comma-separated TEST_SHARD_DATABASE_URLS to run PostgreSQL integration tests")
	}
	urls := strings.Split(shards, ",")
	for i := range urls {
		urls[i] = strings.TrimSpace(urls[i])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := New(ctx, control, urls, 8)
	if err != nil {
		t.Fatal(err)
	}
	f := &integrationFixture{store: s, controlURL: control, shardURLs: urls}
	t.Cleanup(func() {
		// Use independent pools so cleanup still works after reconnect/failure
		// tests close pools. Only this fixture's UUIDs are ever deleted.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, url := range urls {
			pool, err := pgxpool.New(ctx, url)
			if err != nil {
				t.Errorf("cleanup shard connection: %v", err)
				continue
			}
			for _, table := range []string{"poll_counters", "votes"} {
				if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE poll_id::text = ANY($1::text[])", f.pollIDs); err != nil {
					t.Errorf("cleanup %s: %v", table, err)
				}
			}
			pool.Close()
		}
		pool, err := pgxpool.New(ctx, control)
		if err != nil {
			t.Errorf("cleanup control connection: %v", err)
		} else {
			if _, err := pool.Exec(ctx, "DELETE FROM poll_create_requests WHERE poll_id::text = ANY($1::text[])", f.pollIDs); err != nil {
				t.Errorf("cleanup creation keys: %v", err)
			}
			if _, err := pool.Exec(ctx, "DELETE FROM polls WHERE id::text = ANY($1::text[])", f.pollIDs); err != nil {
				t.Errorf("cleanup polls: %v", err)
			}
			pool.Close()
		}
		s.Close()
	})
	return f
}

func testUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func testPoll(id string) model.Poll {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return model.Poll{ID: id, Question: "Choose", Type: "multiple", Options: []string{"A", "B", "C"}, MinChoices: 1, MaxChoices: 3, OpensAt: now.Add(-time.Second), ClosesAt: now.Add(time.Minute)}
}

func (f *integrationFixture) createPoll(t *testing.T) model.Poll {
	t.Helper()
	p := testPoll(testUUID(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, replay, err := f.store.CreatePoll(ctx, p, "test-"+p.ID)
	if err != nil || replay {
		t.Fatalf("create: %+v, replay=%v, error=%v", got, replay, err)
	}
	f.pollIDs = append(f.pollIDs, got.ID)
	return got
}

func testBallot(pollID string, voter int, mask int64) model.Ballot {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:voter:%d", pollID, voter)))
	return model.Ballot{PollID: pollID, Digest: hash[:], Mask: mask, ReceivedAt: time.Now().UTC()}
}

func TestIntegrationForcesDurableCommitSetting(t *testing.T) {
	f := integrationStore(t)
	u, err := url.Parse(f.shardURLs[0])
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("synchronous_commit", "off")
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := openPool(ctx, u.String(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var setting string
	if err = pool.QueryRow(ctx, "SHOW synchronous_commit").Scan(&setting); err != nil || setting != "on" {
		t.Fatalf("durability setting=%q err=%v", setting, err)
	}
}

func TestIntegrationConcurrentCreateIdempotency(t *testing.T) {
	f := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	base := testPoll(testUUID(t))
	key := "test-create-race-" + base.ID
	const callers = 24
	type result struct {
		poll   model.Poll
		replay bool
		err    error
	}
	results := make(chan result, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		p := base
		p.ID = testUUID(t)
		go func(p model.Poll) {
			<-start
			poll, replay, err := f.store.CreatePoll(ctx, p, key)
			results <- result{poll, replay, err}
		}(p)
	}
	close(start)
	created := 0
	var winner string
	for i := 0; i < callers; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if winner == "" {
			winner = r.poll.ID
			f.pollIDs = append(f.pollIDs, winner)
		}
		if r.poll.ID != winner {
			t.Fatalf("idempotency returned different polls %s and %s", winner, r.poll.ID)
		}
		if !r.replay {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("expected one creator, got %d", created)
	}
	conflict := base
	conflict.Question = "Different payload"
	if _, _, err := f.store.CreatePoll(ctx, conflict, key); !errors.Is(err, ErrConflict) {
		t.Fatalf("payload conflict returned %v", err)
	}
	p, err := f.store.GetPoll(ctx, winner)
	if err != nil || p.Question != base.Question {
		t.Fatalf("stored winner changed: %+v, %v", p, err)
	}
	if _, err := f.store.GetPoll(ctx, testUUID(t)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown poll: %v", err)
	}
	listed, err := f.store.ListPolls(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, p := range listed {
		if p.ID == winner {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("winner appears %d times in listing", found)
	}
}

func TestIntegrationConcurrentDuplicateVotes(t *testing.T) {
	f := integrationStore(t)
	p := f.createPoll(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const voters, workers = 64, 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := make([]int, voters)
	errs := make(chan error, workers)
	start := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			ballots := make([]model.Ballot, voters)
			for i := range ballots {
				voter := i
				if worker%2 == 1 {
					voter = voters - 1 - i
				}
				ballots[i] = testBallot(p.ID, voter, 3)
			}
			<-start
			got, err := f.store.WriteBatch(ctx, 0, worker%4, ballots)
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for i, yes := range got {
				if yes {
					voter := i
					if worker%2 == 1 {
						voter = voters - 1 - i
					}
					accepted[voter]++
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for voter, count := range accepted {
		if count != 1 {
			t.Fatalf("voter %d accepted %d times", voter, count)
		}
	}
	got, err := f.store.Results(ctx, p.ID, len(p.Options))
	if err != nil {
		t.Fatal(err)
	}
	want := model.Counts{Total: voters, Options: []int64{voters, voters, 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("counts %+v, want %+v", got, want)
	}
}

func TestIntegrationBatchReplayAndPersistedMultiChoice(t *testing.T) {
	f := integrationStore(t)
	p := f.createPoll(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	expected := model.Counts{Options: make([]int64, 32)}
	for shard := range f.shardURLs {
		ballots := make([]model.Ballot, 0, 50)
		for voter := 0; voter < 48; voter++ {
			mask := int64(1)
			if voter%2 == 1 {
				mask |= 2
				expected.Options[1]++
			}
			if voter%3 == 0 {
				mask |= 1 << 31
				expected.Options[31]++
			}
			expected.Total++
			expected.Options[0]++
			ballots = append(ballots, testBallot(p.ID, shard*1000+voter, mask))
		}
		// The first of a duplicate within the same statement wins, including
		// its original selection. The second must never increment option 2.
		duplicate := ballots[0]
		duplicate.Mask = 4
		ballots = append(ballots, duplicate)
		got, err := f.store.WriteBatch(ctx, shard, shard%4, ballots)
		if err != nil {
			t.Fatal(err)
		}
		for i, accepted := range got {
			if accepted != (i < 48) {
				t.Fatalf("batch acceptance at %d: %v", i, accepted)
			}
		}
		replayed, err := f.store.WriteBatch(ctx, shard, (shard+1)%4, ballots)
		if err != nil {
			t.Fatal(err)
		}
		for i, accepted := range replayed {
			if accepted {
				t.Fatalf("replayed ballot %d accepted", i)
			}
		}
	}
	// A newly connected Store proves that receipts and counters survive pool
	// replacement and do not depend on process-local state.
	f.store.Close()
	reopened, err := New(ctx, f.controlURL, f.shardURLs, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Results(ctx, p.ID, 32)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("persisted counts %+v, want %+v", got, expected)
	}
}

func TestIntegrationRejectedOrCancelledBatchDoesNotCount(t *testing.T) {
	f := integrationStore(t)
	p := f.createPoll(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	valid := testBallot(p.ID, 1, 1)
	invalid := testBallot("invalid-uuid", 2, 1)
	if _, err := f.store.WriteBatch(ctx, 0, 0, []model.Ballot{valid, invalid}); err == nil {
		t.Fatal("invalid UUID batch succeeded")
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, err := f.store.WriteBatch(cancelled, 0, 0, []model.Ballot{valid}); err == nil {
		t.Fatal("already-cancelled write succeeded")
	}
	got, err := f.store.Results(ctx, p.ID, 3)
	if err != nil || got.Total != 0 {
		t.Fatalf("failed batches left counts: %+v, %v", got, err)
	}
	accepted, err := f.store.WriteBatch(ctx, 0, 0, []model.Ballot{valid})
	if err != nil || !accepted[0] {
		t.Fatalf("valid retry failed: %v, %v", accepted, err)
	}
	got, err = f.store.Results(ctx, p.ID, 3)
	if err != nil || got.Total != 1 {
		t.Fatalf("valid retry counts: %+v, %v", got, err)
	}
}

func TestIntegrationCounterFailureRollsBackReceipt(t *testing.T) {
	f := integrationStore(t)
	p := f.createPoll(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Force a database error after receipt insertion by overflowing the total
	// counter. The entire data-modifying CTE must roll back, including receipts.
	_, err := f.store.shards[0].Exec(ctx, `INSERT INTO poll_counters
		(poll_id, lane, option_index, votes) VALUES ($1::uuid, 0, -1, $2)`, p.ID, int64(math.MaxInt64))
	if err != nil {
		t.Fatal(err)
	}
	ballot := testBallot(p.ID, 1, 1)
	if _, err := f.store.WriteBatch(ctx, 0, 0, []model.Ballot{ballot}); err == nil {
		t.Fatal("overflowing counter update succeeded")
	}
	if _, err := f.store.shards[0].Exec(ctx, "DELETE FROM poll_counters WHERE poll_id = $1::uuid", p.ID); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.Results(ctx, p.ID, 3)
	if err != nil || got.Total != 0 || !reflect.DeepEqual(got.Options, []int64{0, 0, 0}) {
		t.Fatalf("failed CTE left counters: %+v, %v", got, err)
	}
	accepted, err := f.store.WriteBatch(ctx, 0, 0, []model.Ballot{ballot})
	if err != nil || !accepted[0] {
		t.Fatalf("receipt from failed CTE survived: %v, %v", accepted, err)
	}
	got, err = f.store.Results(ctx, p.ID, 3)
	if err != nil || got.Total != 1 || !reflect.DeepEqual(got.Options, []int64{1, 0, 0}) {
		t.Fatalf("recovered counts: %+v, %v", got, err)
	}
}

func TestIntegrationCanonicalUUIDBatchReceipts(t *testing.T) {
	f := integrationStore(t)
	p := f.createPoll(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := testBallot(p.ID, 1, 1)
	first.PollID = strings.ToUpper(first.PollID)
	duplicate := first
	duplicate.PollID = p.ID
	duplicate.Mask = 2
	accepted, err := f.store.WriteBatch(ctx, 0, 0, []model.Ballot{first, duplicate})
	if err != nil || !reflect.DeepEqual(accepted, []bool{true, false}) {
		t.Fatalf("canonical UUID receipts: %v, %v", accepted, err)
	}
	got, err := f.store.Results(ctx, p.ID, 3)
	if err != nil || !reflect.DeepEqual(got, model.Counts{Total: 1, Options: []int64{1, 0, 0}}) {
		t.Fatalf("canonical UUID counts: %+v, %v", got, err)
	}
}

func TestIntegrationUnavailableShardFailsResultsAndReadiness(t *testing.T) {
	f := integrationStore(t)
	p := f.createPoll(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f.store.shards[len(f.store.shards)-1].Close()
	got, err := f.store.Results(ctx, p.ID, 3)
	if err == nil || got.Total != 0 || got.Options != nil {
		t.Fatalf("unavailable shard returned partial results: %+v, %v", got, err)
	}
	if err := f.store.Ping(ctx); err == nil {
		t.Fatal("unavailable shard remained ready")
	}
}

func TestIntegrationRejectsAliasedShardDatabase(t *testing.T) {
	f := integrationStore(t)
	aliased, err := url.Parse(f.shardURLs[0])
	if err != nil {
		t.Fatal(err)
	}
	query := aliased.Query()
	query.Set("application_name", "tvpoll-aliased-shard-test")
	aliased.RawQuery = query.Encode()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := New(ctx, f.controlURL, []string{f.shardURLs[0], aliased.String()}, 4)
	if err == nil {
		s.Close()
		t.Fatal("two URLs reaching one database were accepted as independent voting shards")
	}
	if !strings.Contains(err.Error(), "same persisted database identity") {
		t.Fatalf("expected duplicate database identity error, got %v", err)
	}
}
