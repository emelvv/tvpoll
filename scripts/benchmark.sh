#!/usr/bin/env sh
# A local closed-loop benchmark, not a 100M votes/min capacity claim.
set -eu
voters=${1:-10000}
concurrency=${2:-128}
docker compose exec -T server load -mode=benchmark -voters="$voters" -concurrency="$concurrency" -duplicate-every=5
