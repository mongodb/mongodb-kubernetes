# MongoDB Enterprise Ops Manager (Docker)

This directory hosts the Dockerfile and scripts required to run a MongoDB Ops Manager 4.0 instance.

It can be run locally for testing or development purposes (see below) or as part of a Kubernetes deployment.


## Pull the image

You can pull the image using `docker pull quay.io/mongodb/mongodb-enterprise-ops-manager:4.0.0.49984.20180625T1427Z-1`.

After pulling the image (around 1GB so be patient), you can run it with `docker run -t mongodb/mongodb-enterprise-ops-manager:4.0.0.49984.20180625T1427Z-1`.

```bash
# First determine your network IP: 127.0.0.1 or localhost cannot be used since other containers running in Docker
# would not be able to access this host, as each would have its own meaning of localhost
read -ra LOCAL_IPS <<< "$(/sbin/ifconfig | grep -E 'inet[^0-9]+' | sed -E 's/.*inet[^0-9]+([0-9\.]+).*/\1/g' | grep -v 127.0.0.1)"
export OM_HOST="${LOCAL_IPS[0]}"

# Alternatively, the above step can be skipped and you can define your own IP or HOSTNAME (no ports, no http prefix)
#export OM_HOST=ip_or_hostname

docker run --name ops_manager \
    -t "mongodb/mongodb-enterprise-ops-manager:4.0.0.49984.20180625T1427Z-1" \
    -p 8080:8080 \
    -e OM_HOST
```

### Cache Downloads of MMS & MongoDb

The dockerfile expects a few mms and mongodb builds to be downloadable
from your hosts computer, as rapidly iterating with the Docker
environment can get really slow with the 800M mms images.

To help on this, you need to execute `make start_cache_server` before you
build the images. Make this script run in a second terminal, the
relevant images will be downloaded and used by docker build.


### Auto-configuration
On the first time you run the container in your environment, a registration script will be run,
which will create a global owner, a project, and configure a `0.0.0.0/0` whitelist.

The generated values will be stored inside the container and can be imported in your local environment
with `eval '$(docker exec ops_manager cat "${HOME}/.ops-manager-env")'`.

If you would like to run the container without this pre-registration step, start the container with:

```bash
export SKIP_OPS_MANAGER_REGISTRATION=true
docker run --name ops_manager \
    -t "mongodb/mongodb-enterprise-ops-manager:4.0.0.49984.20180625T1427Z-1" \
    -p 8080:8080 \
    -e SKIP_OPS_MANAGER_REGISTRATION \
    -e OM_HOST
```


### Building the image

You can use `make clean build run` to build the container from scratch and then run it.
If you don't want Ops Manager to be automatically configured, see the [auto-configuration](#auto-configuration) section above
(define the `SKIP_OPS_MANAGER_REGISTRATION` enviroment variable before executing `make`).

For more information on the available options, run `make` or read the provided [Makefile](Makefile).


### Other useful commands

**See the running processes in the Ops Manager container:**

```bash
docker exec -t $(docker ps -a -f 'name=ops_manager' | tail -n +2 | awk '{print $1}') ps -ef
```

**Connect to a running container:**

```bash
docker exec -it $(docker ps -a -f 'name=ops_manager' | tail -n +2 | awk '{print $1}') /bin/bash
```
