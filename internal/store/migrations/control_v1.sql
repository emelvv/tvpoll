CREATE TABLE polls (
    id uuid PRIMARY KEY,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE poll_create_requests (
    idempotency_key text PRIMARY KEY,
    payload_sha bytea NOT NULL CHECK (octet_length(payload_sha) = 32),
    poll_id uuid NOT NULL REFERENCES polls(id) DEFERRABLE INITIALLY DEFERRED
);
