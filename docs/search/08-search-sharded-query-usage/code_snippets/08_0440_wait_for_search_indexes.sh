#!/usr/bin/env bash
# Wait for search indexes to be ready
#
# After creating search indexes, mongot needs time to build them.
# A simple sleep is the most reliable wait strategy.

echo "Waiting 2 minutes for search indexes to build..."
sleep 120
echo "✓ Wait complete — proceeding with queries"
