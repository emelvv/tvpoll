#!/usr/bin/env sh
# Run from the repository root after `docker compose up --build -d --wait`.
set -eu
docker compose exec -T server load -mode=smoke -voters=100 -concurrency=16 -duplicate-every=5
