-- Identity follows the physical shard, not its connection URL. Distinct URLs
-- can reach the same database and must not be summed as independent shards.
CREATE TABLE shard_identity (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid()
);
INSERT INTO shard_identity DEFAULT VALUES;

CREATE TABLE votes (
    poll_id uuid NOT NULL,
    voter_digest bytea NOT NULL CHECK (octet_length(voter_digest) = 32),
    mask bigint NOT NULL CHECK (mask > 0 AND mask <= 4294967295),
    received_at timestamptz NOT NULL,
    PRIMARY KEY (poll_id, voter_digest)
);

-- Different ingestion workers update different lanes. Results sum the lanes;
-- no single counter row serializes all votes for a popular poll.
CREATE TABLE poll_counters (
    poll_id uuid NOT NULL,
    lane integer NOT NULL CHECK (lane >= 0),
    option_index integer NOT NULL CHECK (option_index BETWEEN -1 AND 31),
    votes bigint NOT NULL CHECK (votes >= 0),
    PRIMARY KEY (poll_id, lane, option_index)
);
