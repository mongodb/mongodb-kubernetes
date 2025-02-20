#!/usr/bin/env bash

# Note: running directly docker system prune and related commands won't be enough since
# images are accumulated via containerd which is running in docker. So you need to jump into docker and cleanup
# via crictl


# Get all container IDs
container_ids=$(docker ps -q)

# Iterate through each container ID
for container_id in ${container_ids}; do
    echo "Cleaning up container ${container_id}"
    # Use docker exec to run crictl rmi --prune inside the container
    docker exec "${container_id}" crictl rmi --prune
done

echo "Cleanup complete!"
