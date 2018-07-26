# MongoDB Enterprise Database

This directory hosts a Dockerfile that can be run locally for development purposes (see below) or 
as part of a Kubernetes deployment, using the [MongoDB Enterprise Kubernetes Operator](../mongodb-enterprise-operator).

### Running locally

You can use `make clean build run` to build and run the container.

For more details regarding the available options, run `make` or read the provided [Makefile](Makefile).


### Other useful commands

**See the status of all running Automation Agents:**

```bash
for img in $(docker ps -a -f 'ancestor=dev/mongodb-enterprise-database' | tail -n +2 | awk '{print $1}'); do echo; echo "$img"; echo "---"; docker exec -t "$img" ps -ef; echo "---"; done
```

**Connect to a running container:**

```bash
docker exec -it $(docker ps -a -f 'ancestor=dev/mongodb-enterprise-database' | tail -n +2 | awk '{print $1}') /bin/bash
```
