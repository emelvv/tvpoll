#!/usr/bin/env python3
"""Read-only runtime checks. Use a LOCAL poll created by smoke/benchmark."""
import argparse
import json
import os
from datetime import datetime, timezone
from urllib.error import HTTPError
from urllib.request import Request, urlopen

parser = argparse.ArgumentParser()
parser.add_argument("--base", default="http://localhost:8080")
parser.add_argument("--poll", required=True)
parser.add_argument("--expected", type=int, required=True)
parser.add_argument("--stage", required=True)
parser.add_argument("--degraded", action="store_true")
parser.add_argument("--output", default="docs/evidence/runtime.jsonl")
args = parser.parse_args()
token = os.environ.get("ADMIN_TOKEN", "")
if len(token) < 32:
    parser.error("set ADMIN_TOKEN from your local .env")

def get(path, admin=False):
    headers = {"Authorization": "Bearer " + token} if admin else {}
    request = Request(args.base.rstrip("/") + path, headers=headers)
    try:
        response = urlopen(request, timeout=10)
    except HTTPError as error:
        response = error
    with response:
        return response.code, json.load(response)

live, _ = get("/healthz")
ready, _ = get("/readyz")
status, results = get("/api/admin/polls/" + args.poll + "/results", True)
assert live == 200, ("liveness", live)
if args.degraded:
    assert ready == status == 503, (ready, status)
    assert results["error"] == "incomplete_results" and "total_votes" not in results
else:
    assert ready == status == 200, (ready, status)
    assert results["total_votes"] == args.expected, results
    assert sum(results["option_counts"]) == args.expected, results  # single-choice fixture
    public_status, public = get("/api/polls/" + args.poll)
    assert public_status == 200 and public["id"] == args.poll

record = {"at": datetime.now(timezone.utc).isoformat(), "stage": args.stage,
          "healthz": live, "readyz": ready, "results_status": status,
          "total_votes": results.get("total_votes"),
          "option_counts": results.get("option_counts"), "passed": True}
with open(args.output, "a", encoding="utf-8") as file:
    file.write(json.dumps(record, ensure_ascii=False) + "\n")
print(json.dumps(record, ensure_ascii=False))
