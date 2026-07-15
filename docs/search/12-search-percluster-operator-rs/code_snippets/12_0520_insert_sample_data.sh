echo "Inserting sample documents (text + small vectors) through the replica set..."
echo "Written ONCE via a replica-set connection string; every cluster's mongot then"
echo "syncs them independently from its local members."

mdb_script=$(cat <<'EOF'
use sample_search;
db.movies.drop();
// Tiny deterministic dataset: text fields for $search, an 8-dim vector for
// $vectorSearch. Vectors are hand-picked so "space adventure"-like docs cluster
// near [1,0,...] and "cooking" docs near [0,1,...].
db.movies.insertMany([
  { title: "Solar Drift",     plot: "A lone pilot crosses the outer planets chasing a derelict star freighter.", genres: ["SciFi"],   vec: [0.95, 0.05, 0.1, 0.0, 0.2, 0.1, 0.0, 0.1] },
  { title: "Gravity Well",    plot: "Astronauts stranded on a space station must repair the engine before orbit decays.", genres: ["SciFi"], vec: [0.9, 0.1, 0.15, 0.05, 0.1, 0.2, 0.05, 0.0] },
  { title: "The Last Comet",  plot: "A space telescope crew races to catalog a comet before it leaves the solar system.", genres: ["SciFi", "Drama"], vec: [0.85, 0.0, 0.2, 0.1, 0.15, 0.1, 0.1, 0.05] },
  { title: "Sourdough Years", plot: "A retired baker teaches a village to bake bread the old way.", genres: ["Drama"], vec: [0.05, 0.95, 0.1, 0.1, 0.0, 0.05, 0.2, 0.1] },
  { title: "Knife Skills",    plot: "Rival chefs compete in a cooking contest where every dish tells a family story.", genres: ["Drama"], vec: [0.1, 0.9, 0.05, 0.2, 0.1, 0.0, 0.15, 0.05] },
  { title: "Broth",           plot: "A street-food cook perfects a soup recipe passed down for generations.", genres: ["Documentary"], vec: [0.0, 0.85, 0.15, 0.1, 0.05, 0.1, 0.1, 0.2] },
  { title: "Fastball",        plot: "An aging baseball pitcher mentors a rookie through a losing season.", genres: ["Sport", "Drama"], vec: [0.4, 0.4, 0.8, 0.1, 0.1, 0.1, 0.0, 0.0] },
  { title: "The Ninth Inning",plot: "A small-town baseball team makes an improbable playoff run.", genres: ["Sport"], vec: [0.35, 0.35, 0.85, 0.05, 0.1, 0.0, 0.1, 0.1] }
]);
db.movies.countDocuments();
EOF
)

kubectl exec --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" \
  mongodb-tools-pod -- /bin/bash -eu -c "$(cat <<EOF
echo '${mdb_script}' > /tmp/insert.js
mongosh --quiet "${MDB_CONNECTION_STRING}" < /tmp/insert.js
EOF
)"

echo "[ok] sample data inserted"
