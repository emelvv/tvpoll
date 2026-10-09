package store

import (
	"context"
	"testing"
	"time"

	"github.com/emelvv/tvpoll/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCanonicalPayloadIgnoresIDAndTimeZone(t *testing.T) {
	instant := time.Date(2026, 10, 9, 9, 0, 0, 123456789, time.UTC)
	a := model.Poll{ID: "first", Question: "Question?", Type: "single", Options: []string{"A", "B"}, MinChoices: 1, MaxChoices: 1, OpensAt: instant, ClosesAt: instant.Add(time.Minute)}
	b := a
	b.ID = "second"
	b.OpensAt = b.OpensAt.In(time.FixedZone("MSK", 3*60*60))
	b.ClosesAt = b.ClosesAt.In(time.FixedZone("MSK", 3*60*60))
	first, err := canonicalPayload(a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalPayload(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("equivalent creation payloads differ: %s != %s", first, second)
	}
	b.Options = []string{"B", "A"}
	reordered, err := canonicalPayload(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) == string(reordered) {
		t.Fatal("option ordering must affect idempotency payload")
	}
}

func TestRejectInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		shards []string
		conns  int32
	}{{nil, 1}, {[]string{"postgres://invalid"}, 0}, {[]string{"postgres://invalid"}, -1}} {
		if _, err := New(context.Background(), "postgres://invalid", tc.shards, tc.conns); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}

func TestWriteBatchRejectsInvalidBallotBeforeConnecting(t *testing.T) {
	s := &Store{shards: []*pgxpool.Pool{nil}}
	for _, ballot := range []model.Ballot{
		{Digest: make([]byte, 31), Mask: 1},
		{Digest: make([]byte, 32), Mask: 0},
		{Digest: make([]byte, 32), Mask: 1 << 32},
	} {
		if _, err := s.WriteBatch(context.Background(), 0, 0, []model.Ballot{ballot}); err == nil {
			t.Fatal("invalid ballot accepted")
		}
	}
	if _, err := s.WriteBatch(context.Background(), -1, 0, nil); err == nil {
		t.Fatal("negative shard accepted")
	}
	if _, err := s.WriteBatch(context.Background(), 0, -1, nil); err == nil {
		t.Fatal("negative lane accepted")
	}
	got, err := s.WriteBatch(context.Background(), 0, 0, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty batch: %v, %v", got, err)
	}
}
