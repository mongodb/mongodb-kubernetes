# CI assertion layer for the functional steps (12_0500-12_0540): re-runs the same
# per-cluster queries quietly and exits non-zero on any unexpected result. This is
# deliberately NOT part of the tutorial -- READMEs and guides must not reference it.
# The tutorial-facing demonstration of these queries is 12_0540.

assert_script=$(cat <<'EOF'
function fail(msg) { throw new Error("ASSERTION FAILED: " + msg); }

// The per-process mongotHost must point at the proxy service of THIS cluster
// (locality), not merely at any reachable mongot.
const p = db.adminCommand({ getParameter: 1, mongotHost: 1 });
if (p.mongotHost !== expectedMongotHost) {
  fail("mongotHost is " + JSON.stringify(p.mongotHost) + ", expected " + JSON.stringify(expectedMongotHost));
}

const sdb = db.getSiblingDB("sample_search");

const textTitles = sdb.movies.aggregate([
  { $search: { text: { query: "baseball", path: "plot" } } },
  { $project: { _id: 0, title: 1 } }
]).toArray().map(d => d.title).sort();
const expectedTitles = ["Fastball", "The Ninth Inning"];
if (JSON.stringify(textTitles) !== JSON.stringify(expectedTitles)) {
  fail("$search baseball returned " + JSON.stringify(textTitles) + ", expected " + JSON.stringify(expectedTitles));
}

const vec = sdb.movies.aggregate([
  { $vectorSearch: { index: "vector_index", path: "vec",
      queryVector: [1, 0, 0.1, 0, 0.1, 0.1, 0, 0.1], numCandidates: 8, limit: 1 } },
  { $project: { _id: 0, title: 1 } }
]).toArray();
const spaceTitles = ["Solar Drift", "Gravity Well"];
if (vec.length !== 1 || !spaceTitles.includes(vec[0].title)) {
  fail("$vectorSearch top hit was " + JSON.stringify(vec) + ", expected one of " + JSON.stringify(spaceTitles));
}

print("assertions passed");
EOF
)

for i in 0 1 2; do
  ctx_var="K8S_CLUSTER_${i}_CONTEXT_NAME"
  ctx="${!ctx_var}"
  proxy_var="SEARCH_PROXY_SVC_${i}"
  expected_mongot_host="${!proxy_var}:${ENVOY_PROXY_PORT}"
  member="${RS_RESOURCE_NAME}-${i}-0-svc.${MDB_NAMESPACE}.svc.cluster.local"
  local_uri="mongodb://${SEARCH_ADMIN_USER_NAME}:${SEARCH_ADMIN_USER_PASSWORD}@${member}:27017/?directConnection=true&authSource=admin&readPreference=secondaryPreferred&tls=true&tlsCAFile=/tls/ca.crt"

  echo "asserting mongotHost locality and search results on ${ctx} (cluster index ${i})..."
  kubectl exec --context "${ctx}" -n "${MDB_NAMESPACE}" mongodb-tools-pod -- /bin/bash -eu -c "$(cat <<EOF
echo 'const expectedMongotHost = "${expected_mongot_host}";' > /tmp/assert.js
echo '${assert_script}' >> /tmp/assert.js
mongosh --quiet "${local_uri}" < /tmp/assert.js
EOF
)"
done

echo "[ok] all three clusters answered with correct results from their own local mongot"
