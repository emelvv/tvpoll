package ingest

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emelvv/tvpoll/internal/model"
)

var ErrBusy = errors.New("vote queue full or draining")

type Writer interface {
	WriteBatch(context.Context, int, int, []model.Ballot) ([]bool, error)
	Shards() int
}
type Result struct {
	Accepted bool
	Err      error
}
type job struct {
	ballot model.Ballot
	reply  chan Result
}
type Metrics struct {
	Accepted, Duplicate, Rejected, Errors, Batches atomic.Uint64
	Queued                                         atomic.Int64
}
type Ingest struct {
	store         Writer
	queues        []chan job
	size          int
	wait, timeout time.Duration
	mu            sync.RWMutex
	closed        bool
	wg            sync.WaitGroup
	Metrics       Metrics
}

func New(store Writer, size, workers, queueSize int, wait, timeout time.Duration) *Ingest {
	i := &Ingest{store: store, size: size, wait: wait, timeout: timeout}
	for s := 0; s < store.Shards(); s++ {
		q := make(chan job, queueSize)
		i.queues = append(i.queues, q)
		for w := 0; w < workers; w++ {
			i.wg.Add(1)
			go i.worker(s, w, q)
		}
	}
	return i
}

// Enqueue never waits for capacity. The caller may leave; an admitted ballot still
// commits, allowing safe retries after an ambiguous HTTP response.
func (i *Ingest) Enqueue(b model.Ballot) (<-chan Result, error) {
	if len(b.Digest) != 32 {
		return nil, errors.New("invalid voter digest")
	}
	s := int(binary.BigEndian.Uint64(b.Digest[:8]) % uint64(len(i.queues)))
	j := job{ballot: b, reply: make(chan Result, 1)}
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.closed {
		i.Metrics.Rejected.Add(1)
		return nil, ErrBusy
	}
	// Increment before publishing, so a fast worker cannot make the gauge negative.
	i.Metrics.Queued.Add(1)
	select {
	case i.queues[s] <- j:
		return j.reply, nil
	default:
		i.Metrics.Queued.Add(-1)
		i.Metrics.Rejected.Add(1)
		return nil, ErrBusy
	}
}

func (i *Ingest) Close() {
	i.mu.Lock()
	if !i.closed {
		i.closed = true
		for _, q := range i.queues {
			close(q)
		}
	}
	i.mu.Unlock()
	i.wg.Wait()
}

func (i *Ingest) worker(shard, lane int, q <-chan job) {
	defer i.wg.Done()
	for first := range q {
		batch := make([]job, 0, i.size)
		batch = append(batch, first)
		timer := time.NewTimer(i.wait)
	collect:
		for len(batch) < i.size {
			select {
			case j, ok := <-q:
				if !ok {
					break collect
				}
				batch = append(batch, j)
			case <-timer.C:
				break collect
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		ballots := make([]model.Ballot, len(batch))
		for n, j := range batch {
			ballots[n] = j.ballot
		}
		ctx, cancel := context.WithTimeout(context.Background(), i.timeout)
		accepted, err := i.store.WriteBatch(ctx, shard, lane, ballots)
		cancel()
		i.Metrics.Batches.Add(1)
		if err == nil && len(accepted) != len(batch) {
			err = errors.New("invalid storage response")
		}
		for n, j := range batch {
			r := Result{Err: err}
			if err != nil {
				i.Metrics.Errors.Add(1)
			} else {
				r.Accepted = accepted[n]
				if r.Accepted {
					i.Metrics.Accepted.Add(1)
				} else {
					i.Metrics.Duplicate.Add(1)
				}
			}
			j.reply <- r
			i.Metrics.Queued.Add(-1)
		}
	}
}
