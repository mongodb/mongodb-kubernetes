#!/usr/bin/env bash
#
# Submit the Phase 2 G2 acceptance Evergreen patch.
#
# Coverage:
#   - e2e_search_q2_mc_rs_steady (the new MC RS test)
#   - 4 SC search regression tests on two non-MC variants (large + small kind)
#
# Variants:
#   - e2e_static_multi_cluster_2_clusters       — runs the MC task group
#   - e2e_static_mdb_kind_ubi_cloudqa           — runs SC search non-large tasks
#   - e2e_static_mdb_kind_ubi_cloudqa_large     — runs SC search large tasks
#
# Prerequisite: `evergreen login` must have been completed in this session
# (the CLI now requires OAuth via browser device-code flow). This script does
# NOT attempt OAuth itself — confirm `evergreen client get-oauth-token` works
# before running this.
#
# Branch: mc-search-phase2-q2-rs at HEAD 90d9cad2a (or later if more commits land).
# Run this from the worktree that has that branch checked out, OR from the
# main repo with that branch checked out via `git checkout mc-search-phase2-q2-rs`.

set -euo pipefail

DESC="${1:-Phase 2 G2: Q2-RS-MC + Envoy CM volume fix + SC regression}"

evergreen patch \
  --project mongodb-kubernetes \
  --variants e2e_static_multi_cluster_2_clusters,e2e_static_mdb_kind_ubi_cloudqa,e2e_static_mdb_kind_ubi_cloudqa_large \
  --tasks \
e2e_search_q2_mc_rs_steady,\
e2e_search_replicaset_external_mongodb_multi_mongot_managed_lb,\
e2e_search_replicaset_external_mongodb_multi_mongot_unmanaged_lb,\
e2e_search_sharded_external_mongodb_multi_mongot_unmanaged_lb,\
e2e_search_sharded_enterprise_external_mongod_managed_lb \
  --finalize \
  --skip_confirm \
  --description "${DESC}"
