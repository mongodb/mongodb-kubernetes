#!/usr/bin/env bash
# Spreads concurrent CI tasks apart. Up to 100 tasks start at once, and every one
# of them downloads from GitHub onto a fresh host, so one random offset per task
# is enough. Hosts are never reused, so this needs no state between tasks.
set -Eeou pipefail

jitter=$((RANDOM % 31))
echo "Adding ${jitter}s jitter to spread out concurrent downloads..."
sleep "${jitter}"
