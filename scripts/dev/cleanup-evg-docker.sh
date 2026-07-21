#!/usr/bin/env bash

# docker system prune isn't enough: images accumulate in the containerd
# running inside each kind node container, so prune them via crictl per node.

container_ids=$(docker ps -q)

for container_id in ${container_ids}; do
    echo "Cleaning up container ${container_id}"
    docker exec "${container_id}" crictl rmi --prune
done

echo "Cleanup complete!"
