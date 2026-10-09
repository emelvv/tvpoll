// Package store persists immutable poll metadata and sharded, deduplicated votes.
package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/emelvv/tvpoll/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = model.ErrNotFound
	ErrConflict = model.ErrConflict
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store owns its pools. maxConns applies independently to the control database
// and every voting shard; aggregate pool use is (1 + shard count) * maxConns.
type Store struct {
	control *pgxpool.Pool
	shards  []*pgxpool.Pool
}

func New(ctx context.Context, controlURL string, shardURLs []string, maxConns int32) (*Store, error) {
	if maxConns <= 0 {
		return nil, errors.New("database max connections must be positive")
	}
	if len(shardURLs) == 0 {
		return nil, errors.New("at least one voting shard is required")
	}
	s := &Store{}
	var err error
	s.control, err = openPool(ctx, controlURL, maxConns)
	if err != nil {
		return nil, fmt.Errorf("control database: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()
	if err := migrate(ctx, s.control, "control", 0x5456504f4c4c01); err != nil {
		return nil, fmt.Errorf("control migration: %w", err)
	}
	seenShardIDs := make(map[string]int, len(shardURLs))
	for i, url := range shardURLs {
		pool, err := openPool(ctx, url, maxConns)
		if err != nil {
			return nil, fmt.Errorf("voting shard %d: %w", i, err)
		}
		s.shards = append(s.shards, pool)
		if err := migrate(ctx, pool, "shard", 0x5456504f4c4c02); err != nil {
			return nil, fmt.Errorf("voting shard %d migration: %w", i, err)
		}
		var identity string
		if err := pool.QueryRow(ctx, "SELECT id::text FROM shard_identity WHERE singleton = true").Scan(&identity); err != nil {
			return nil, fmt.Errorf("voting shard %d identity: %w", i, err)
		}
		if previous, exists := seenShardIDs[identity]; exists {
			return nil, fmt.Errorf("voting shards %d and %d resolve to the same persisted database identity", previous, i)
		}
		seenShardIDs[identity] = i
	}
	ok = true
	return s, nil
}

func openPool(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	config.MaxConns = maxConns
	config.MinConns = 0
	// Successful API acknowledgements require WAL flush on the primary, even
	// when a connection URL supplied a weaker synchronous_commit setting.
	config.ConnConfig.RuntimeParams["synchronous_commit"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func migrate(ctx context.Context, pool *pgxpool.Pool, role string, lock int64) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer rollback(tx) // Best effort; Commit closes the tx.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lock); err != nil {
		return err
	}
	// role is an internal constant, never request-controlled SQL.
	table := "tvpoll_" + role + "_migrations"
	if _, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	var version int
	if err := tx.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM "+table).Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("database schema version %d is newer than supported version 1", version)
	}
	if version == 0 {
		sql, err := migrations.ReadFile("migrations/" + role + "_v1.sql")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO "+table+" (version) VALUES (1)"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) Close() {
	for _, pool := range s.shards {
		pool.Close()
	}
	if s.control != nil {
		s.control.Close()
	}
}

func (s *Store) Shards() int { return len(s.shards) }

// Ping checks every dependency. Partial shard availability is not readiness.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.control.Ping(ctx); err != nil {
		return fmt.Errorf("control database: %w", err)
	}
	return s.eachShard(ctx, func(ctx context.Context, i int, pool *pgxpool.Pool) error {
		return pool.Ping(ctx)
	})
}

// canonicalPayload excludes the newly generated ID. Equivalent timestamp
// instants use UTC, while option ordering remains part of the poll's identity.
func canonicalPayload(p model.Poll) ([]byte, error) {
	return json.Marshal(struct {
		Question   string    `json:"question"`
		Type       string    `json:"type"`
		Options    []string  `json:"options"`
		MinChoices int       `json:"min_choices"`
		MaxChoices int       `json:"max_choices"`
		OpensAt    time.Time `json:"opens_at"`
		ClosesAt   time.Time `json:"closes_at"`
	}{p.Question, p.Type, p.Options, p.MinChoices, p.MaxChoices, p.OpensAt.UTC(), p.ClosesAt.UTC()})
}

// CreatePoll atomically reserves an idempotency key and creates immutable
// metadata. Concurrent callers with an identical payload receive the winner's
// poll; different payloads receive ErrConflict, even when their generated IDs
// differ. An unknown commit outcome can safely be retried with the same key.
func (s *Store) CreatePoll(ctx context.Context, p model.Poll, key string) (model.Poll, bool, error) {
	if key == "" {
		return model.Poll{}, false, errors.New("idempotency key must not be empty")
	}
	payload, err := canonicalPayload(p)
	if err != nil {
		return model.Poll{}, false, err
	}
	hash := sha256.Sum256(payload)
	tx, err := s.control.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return model.Poll{}, false, err
	}
	defer rollback(tx)
	var reservedID string
	err = tx.QueryRow(ctx, `
		INSERT INTO poll_create_requests (idempotency_key, payload_sha, poll_id)
		VALUES ($1, $2, $3::uuid)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING poll_id::text`, key, hash[:], p.ID).Scan(&reservedID)
	if errors.Is(err, pgx.ErrNoRows) {
		var existingHash []byte
		var body []byte
		// A separate statement is essential: READ COMMITTED sees the winning
		// transaction after the conflicting INSERT has waited for it to commit.
		err := tx.QueryRow(ctx, `
			SELECT r.payload_sha, p.payload FROM poll_create_requests r
			JOIN polls p ON p.id = r.poll_id WHERE r.idempotency_key = $1`, key).Scan(&existingHash, &body)
		if err != nil {
			return model.Poll{}, false, err
		}
		if !bytes.Equal(existingHash, hash[:]) {
			return model.Poll{}, false, ErrConflict
		}
		var existing model.Poll
		if err := json.Unmarshal(body, &existing); err != nil {
			return model.Poll{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return model.Poll{}, false, err
		}
		return existing, true, nil
	}
	if err != nil {
		return model.Poll{}, false, err
	}
	// Use the database's canonical UUID representation in persisted responses.
	p.ID = reservedID
	p.OpensAt, p.ClosesAt = p.OpensAt.UTC(), p.ClosesAt.UTC()
	body, err := json.Marshal(p)
	if err != nil {
		return model.Poll{}, false, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO polls (id, payload) VALUES ($1::uuid, $2::jsonb)", p.ID, body); err != nil {
		return model.Poll{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Poll{}, false, err
	}
	return p, false, nil
}

func (s *Store) GetPoll(ctx context.Context, id string) (model.Poll, error) {
	var body []byte
	if err := s.control.QueryRow(ctx, "SELECT payload FROM polls WHERE id = $1::uuid", id).Scan(&body); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.Poll{}, ErrNotFound
		}
		return model.Poll{}, err
	}
	var p model.Poll
	if err := json.Unmarshal(body, &p); err != nil {
		return model.Poll{}, err
	}
	return p, nil
}

func (s *Store) ListPolls(ctx context.Context) ([]model.Poll, error) {
	rows, err := s.control.Query(ctx, "SELECT payload FROM polls ORDER BY created_at DESC, id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	polls := make([]model.Poll, 0)
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var p model.Poll
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		polls = append(polls, p)
	}
	return polls, rows.Err()
}

const writeBatchSQL = `
WITH input AS (
    SELECT poll_id::uuid AS poll_id, voter_digest, mask, received_at, ordinal
    FROM unnest($1::text[], $2::bytea[], $3::bigint[], $4::timestamptz[])
         WITH ORDINALITY AS b(poll_id, voter_digest, mask, received_at, ordinal)
), unique_input AS (
    SELECT DISTINCT ON (poll_id, voter_digest) poll_id, voter_digest, mask, received_at
    FROM input ORDER BY poll_id, voter_digest, ordinal
), inserted AS (
    INSERT INTO votes (poll_id, voter_digest, mask, received_at)
    SELECT poll_id, voter_digest, mask, received_at FROM unique_input
    ORDER BY poll_id, voter_digest
    ON CONFLICT (poll_id, voter_digest) DO NOTHING
    RETURNING poll_id, voter_digest, mask
), deltas AS (
    SELECT poll_id, -1 AS option_index, count(*) AS votes FROM inserted GROUP BY poll_id
    UNION ALL
    SELECT i.poll_id, bit AS option_index, count(*) AS votes
    FROM inserted i CROSS JOIN generate_series(0, 31) AS bit
    WHERE (i.mask & (1::bigint << bit)) <> 0 GROUP BY i.poll_id, bit
), counter_update AS (
    INSERT INTO poll_counters (poll_id, lane, option_index, votes)
    SELECT poll_id, $5::integer, option_index, votes FROM deltas
    ORDER BY poll_id, option_index
    ON CONFLICT (poll_id, lane, option_index)
    DO UPDATE SET votes = poll_counters.votes + EXCLUDED.votes
    RETURNING poll_id
)
SELECT poll_id::text, voter_digest FROM inserted`

// WriteBatch commits vote receipts and all counter increments in a single SQL
// statement. Only rows returned by INSERT count as accepted; existing rows are
// never inferred by SELECT. Replaying an uncertain write cannot increment twice.
// Every voter must always be routed to the same shard by the caller.
func (s *Store) WriteBatch(ctx context.Context, shard, lane int, ballots []model.Ballot) ([]bool, error) {
	if shard < 0 || shard >= len(s.shards) {
		return nil, errors.New("voting shard index out of range")
	}
	if lane < 0 || int64(lane) > math.MaxInt32 {
		return nil, errors.New("counter lane index out of range")
	}
	accepted := make([]bool, len(ballots))
	if len(ballots) == 0 {
		return accepted, nil
	}
	ids := make([]string, len(ballots))
	digests := make([][]byte, len(ballots))
	masks := make([]int64, len(ballots))
	times := make([]time.Time, len(ballots))
	type voterKey struct{ poll, digest string }
	first := make(map[voterKey]int, len(ballots))
	for i, b := range ballots {
		if len(b.Digest) != sha256.Size || b.Mask <= 0 || b.Mask > math.MaxUint32 {
			return nil, fmt.Errorf("invalid ballot at index %d", i)
		}
		// Canonicalize UUIDs before building the receipt map; PostgreSQL returns
		// lowercase UUIDs even if the caller supplied uppercase or compact text.
		var id pgtype.UUID
		if err := id.Scan(b.PollID); err != nil || !id.Valid {
			return nil, fmt.Errorf("invalid ballot poll ID at index %d", i)
		}
		canonicalID := fmt.Sprintf("%x-%x-%x-%x-%x", id.Bytes[:4], id.Bytes[4:6], id.Bytes[6:8], id.Bytes[8:10], id.Bytes[10:])
		ids[i], digests[i], masks[i], times[i] = canonicalID, b.Digest, b.Mask, b.ReceivedAt
		k := voterKey{canonicalID, string(b.Digest)}
		if _, exists := first[k]; !exists {
			first[k] = i
		}
	}
	rows, err := s.shards[shard].Query(ctx, writeBatchSQL, ids, digests, masks, times, int32(lane))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var digest []byte
		if err := rows.Scan(&id, &digest); err != nil {
			return nil, err
		}
		index, exists := first[voterKey{id, string(digest)}]
		if !exists {
			return nil, errors.New("database returned an unexpected voter receipt")
		}
		accepted[index] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return accepted, nil
}

// Results sums a read on each shard. These reads are not a globally synchronized
// snapshot while voting is in flight. Closed, drained polls have final counts.
// A failed shard returns an error, never silently partial results.
func (s *Store) Results(ctx context.Context, pollID string, options int) (model.Counts, error) {
	if options < 1 || options > 32 {
		return model.Counts{}, errors.New("option count must be between 1 and 32")
	}
	parts := make([]model.Counts, len(s.shards))
	err := s.eachShard(ctx, func(ctx context.Context, i int, pool *pgxpool.Pool) error {
		rows, err := pool.Query(ctx, `SELECT option_index, sum(votes)::bigint
			FROM poll_counters WHERE poll_id = $1::uuid GROUP BY option_index`, pollID)
		if err != nil {
			return err
		}
		defer rows.Close()
		part := model.Counts{Options: make([]int64, options)}
		for rows.Next() {
			var index int
			var count int64
			if err := rows.Scan(&index, &count); err != nil {
				return err
			}
			if index == -1 {
				part.Total = count
			} else if index >= 0 && index < options {
				part.Options[index] = count
			} else {
				return fmt.Errorf("stored option index %d is outside poll option count %d", index, options)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		parts[i] = part
		return nil
	})
	if err != nil {
		return model.Counts{}, err
	}
	result := model.Counts{Options: make([]int64, options)}
	for _, part := range parts {
		if part.Total > math.MaxInt64-result.Total {
			return model.Counts{}, errors.New("total vote count overflow")
		}
		result.Total += part.Total
		for i, count := range part.Options {
			if count > math.MaxInt64-result.Options[i] {
				return model.Counts{}, errors.New("option vote count overflow")
			}
			result.Options[i] += count
		}
	}
	return result, nil
}

func (s *Store) eachShard(ctx context.Context, fn func(context.Context, int, *pgxpool.Pool) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	for i, pool := range s.shards {
		wg.Add(1)
		go func(i int, pool *pgxpool.Pool) {
			defer wg.Done()
			if err := fn(ctx, i, pool); err != nil {
				once.Do(func() {
					firstErr = fmt.Errorf("voting shard %d: %w", i, err)
					cancel()
				})
			}
		}(i, pool)
	}
	wg.Wait()
	return firstErr
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
