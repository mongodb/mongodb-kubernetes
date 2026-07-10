#!/bin/bash

set -euo pipefail

# proxy.golang.org egress from the EVG host flakes with HTTP/2 stream resets on a
# cold module cache; force HTTP/1.1 and retry with backoff.
export GODEBUG="${GODEBUG:+${GODEBUG},}http2client=0"
for attempt in 1 2 3 4 5; do
  if go mod download; then
    exit 0
  fi
  echo "go mod download failed (attempt ${attempt}/5); retrying in $((attempt * 5))s..." >&2
  sleep "$((attempt * 5))"
done
echo "go mod download failed after 5 attempts" >&2
exit 1
