package ingest

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/emelvv/tvpoll/internal/model"
)

type writer struct {
	mu      sync.Mutex
	seen    map[string]bool
	calls   int
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
	fail    bool
	shards  []int
}

func (w *writer) Shards() int { return 2 }
func (w *writer) WriteBatch(ctx context.Context, s, l int, b []model.Ballot) ([]bool, error) {
	if w.entered != nil {
		w.once.Do(func() { close(w.entered) })
	}
	if w.gate != nil {
		select {
		case <-w.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	w.shards = append(w.shards, s)
	if w.fail {
		return nil, errors.New("commit uncertain")
	}
	if w.seen == nil {
		w.seen = map[string]bool{}
	}
	out := make([]bool, len(b))
	for n, v := range b {
		key := string(v.Digest)
		out[n] = !w.seen[key]
		w.seen[key] = true
	}
	return out, nil
}
func ballot(n uint64) model.Ballot {
	d := make([]byte, 32)
	binary.BigEndian.PutUint64(d[:8], n)
	return model.Ballot{Digest: d, Mask: 1}
}
func await(t *testing.T, c <-chan Result) Result {
	t.Helper()
	select {
	case r := <-c:
		return r
	case <-time.After(time.Second):
		t.Fatal("no result")
		return Result{}
	}
}
func TestBatchDrainsAndRoutesStableRetries(t *testing.T) {
	w := &writer{}
	i := New(w, 64, 1, 128, 5*time.Millisecond, time.Second)
	var pending []<-chan Result
	for n := uint64(0); n < 100; n++ {
		r, e := i.Enqueue(ballot(n))
		if e != nil {
			t.Fatal(e)
		}
		pending = append(pending, r)
	}
	i.Close()
	for _, r := range pending {
		if !await(t, r).Accepted {
			t.Fatal("lost admitted ballot")
		}
	}
	if i.Metrics.Accepted.Load() != 100 || i.Metrics.Queued.Load() != 0 || w.calls >= 100 {
		t.Fatal("not batched or drained")
	}
	if _, err := i.Enqueue(ballot(999)); !errors.Is(err, ErrBusy) {
		t.Fatal("enqueue after drain")
	}
}
func TestBackpressureAndUnknownOutcome(t *testing.T) {
	w := &writer{gate: make(chan struct{}), entered: make(chan struct{}), fail: true}
	i := New(w, 1, 1, 1, time.Millisecond, time.Second)
	r, _ := i.Enqueue(ballot(0))
	<-w.entered
	r2, err := i.Enqueue(ballot(2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = i.Enqueue(ballot(4)); !errors.Is(err, ErrBusy) {
		t.Fatal("full queue must reject immediately")
	}
	close(w.gate)
	if await(t, r).Err == nil || await(t, r2).Err == nil {
		t.Fatal("failed batch acknowledged")
	}
	i.Close()
	if i.Metrics.Errors.Load() != 2 || i.Metrics.Rejected.Load() != 1 {
		t.Fatal("incorrect failure counters")
	}
}
func TestConcurrentCloseEnqueue(t *testing.T) {
	i := New(&writer{}, 8, 2, 64, time.Millisecond, time.Second)
	var wg sync.WaitGroup
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				_, _ = i.Enqueue(ballot(uint64(n*30 + j)))
			}
		}(n)
	}
	i.Close()
	wg.Wait()
	if i.Metrics.Queued.Load() != 0 {
		t.Fatal("undrained queue")
	}
}
