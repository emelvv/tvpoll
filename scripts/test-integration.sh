#!/usr/bin/env sh
set -eu
# These are the disposable LOCAL Compose databases, never a production URL.
export TEST_CONTROL_DATABASE_URL='postgres://tvpoll:local-only-database-password@localhost:15432/tvpoll?sslmode=disable'
export TEST_SHARD_DATABASE_URLS='postgres://tvpoll:local-only-database-password@localhost:15433/tvpoll?sslmode=disable,postgres://tvpoll:local-only-database-password@localhost:15434/tvpoll?sslmode=disable'
go test -race -count=1 -cover ./...
