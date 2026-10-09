package model

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("idempotency key reused with different payload")
)

type Poll struct {
	ID         string    `json:"id"`
	Question   string    `json:"question"`
	Type       string    `json:"type"`
	Options    []string  `json:"options"`
	MinChoices int       `json:"min_choices"`
	MaxChoices int       `json:"max_choices"`
	OpensAt    time.Time `json:"opens_at"`
	ClosesAt   time.Time `json:"closes_at"`
}

type Ballot struct {
	PollID     string
	Digest     []byte
	Mask       int64
	ReceivedAt time.Time
}

type Counts struct {
	Total   int64   `json:"total_votes"`
	Options []int64 `json:"option_counts"`
}
