# Import sample movie data directly to each shard
# For sharded clusters, we import data directly to each shard pod

for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  pod_name="${shard_name}-0"
  
  echo "Importing data to shard ${i} (pod: ${pod_name})..."
  
  kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" "${pod_name}" -- /bin/bash -c '
    /var/lib/mongodb-mms-automation/mongosh-linux-x86_64-*/bin/mongosh \
      --username mdb-admin \
      --password "'"${MDB_ADMIN_USER_PASSWORD}"'" \
      --authenticationDatabase admin \
      --quiet \
      --eval "
        const movies = [
          { title: \"The Matrix\", year: 1999, plot: \"A computer hacker learns about the true nature of reality\", genres: [\"Action\", \"Sci-Fi\"] },
          { title: \"The Matrix Reloaded\", year: 2003, plot: \"Neo and the rebel leaders continue their fight against the machines\", genres: [\"Action\", \"Sci-Fi\"] },
          { title: \"Inception\", year: 2010, plot: \"A thief who steals corporate secrets through dream-sharing technology\", genres: [\"Action\", \"Sci-Fi\", \"Thriller\"] },
          { title: \"Interstellar\", year: 2014, plot: \"A team of explorers travel through a wormhole in space\", genres: [\"Adventure\", \"Drama\", \"Sci-Fi\"] },
          { title: \"The Matrix Revolutions\", year: 2003, plot: \"The human city of Zion defends itself against the massive invasion\", genres: [\"Action\", \"Sci-Fi\"] }
        ];
        
        db.getSiblingDB(\"sample_mflix\").movies.insertMany(movies);
        print(\"Inserted \" + movies.length + \" movies to shard '"${i}"'\");
      "
  '
done

