IMAGE = mongodb-enterprise-ops-manager
IMAGE_VERSION = 4.0.5.50245.20181031T0042Z-1
IMAGE_REVISION = _test
CONTAINER = ops_manager
MONGODB_VERSION = 4.0.0
AWS_IMAGE_HOST = 268558157000.dkr.ecr.us-east-1.amazonaws.com
AWS_IMAGE_REPO = dev
AWS_REGION = us-east-1
OM_DOWNLOAD_LOCATION = https://downloads.mongodb.com/on-prem-mms/deb
MDB_DOWNLOAD_LOCATION = https://fastdl.mongodb.org/linux

usage:
	@ echo "MongoDB Enterprise Ops Manager - Docker image builder"
	@ echo
	@ echo "Usage:"
	@ echo "  delete_container:      stops and deletes the container"
	@ echo "  clean:                 deletes the image and any running containers"
	@ echo "  build:                 builds the Docker image"
	@ echo "  build_cached           builds the Docker image using cached versions of MMS and Mongodb"
	@ echo "  clean_build:           rebuilds the Docker image from scratch (no-cache), stopping any running containers and deleting all intermediate containers"
	@ echo "  all:                   shorthand for 'clean build run'"
	@ echo "  cache_server:          starts a cache server to avoid downloading of mms and mongodb packages more than once -- Requires Python 3."
	@ echo "  pull:                  pulls the image from the configured Docker registry"
	@ echo "  push:                  pushes the image to the configured Docker registry"
	@ echo
	@ echo "  run:                   runs the image in a new container"
	@ echo "  start:                 (re)starts an existing container"
	@ echo "  stop:                  stops a running container, if one exists"
	@ echo "  restart:               stops any running containers, then restarts"
	@ echo "  status:                shows the running container's IP and processes"
	@ echo
	@ echo "  wait:                  wait until Ops Manager has started and was configured (Docker only, not Kubernetes)"
	@ echo "  get_om_env:            prints the Ops Manager ENV variables for connecting to the running container (Docker only, not Kubernetes)"
	@ echo "  version:               prints the image's version string (IMAGE_VERSION)"
	@ echo

delete_container:
	@ docker rm -f $(CONTAINER) || true

clean: delete_container
	@ docker rmi $(IMAGE):$(IMAGE_VERSION)$(IMAGE_REVISION) || true

cache_server:
	@ echo "Running the cache server. Keep this terminal open"
	@ MONGODB_VERSION=$(MONGODB_VERSION) IMAGE_VERSION=$(IMAGE_VERSION) scripts/cache_server.py

build_cached:
	@ echo "Building using cached MMS and Mongodb"
	$(MAKE) build OM_DOWNLOAD_LOCATION=http://localhost:8000/cache MDB_DOWNLOAD_LOCATION=http://localhost:8000/cache

build:
	$(eval DOCKER_HOST_IP := $(shell scripts/get-local-ip.sh))
	docker build $(EXTRA_PARAM) --add-host="localhost:$(DOCKER_HOST_IP)" \
		-t $(IMAGE):$(IMAGE_VERSION)$(IMAGE_REVISION) \
		--build-arg MONGODB_VERSION=$(MONGODB_VERSION) \
		--build-arg OPS_MANAGER_VERSION=$(IMAGE_VERSION) \
		--build-arg OM_DOWNLOAD_LOCATION=$(OM_DOWNLOAD_LOCATION) \
		--build-arg MDB_DOWNLOAD_LOCATION=$(MDB_DOWNLOAD_LOCATION) \
		.

clean_build:
	@ rm -rf cache/*
	@ $(MAKE) EXTRA_PARAM="--no-cache --force-rm --rm" clean build

all:
	@ $(MAKE) clean build run

aws_login:
	@ eval "$(shell aws ecr get-login --no-include-email --region $(AWS_REGION))"

pull: aws_login
	docker pull $(AWS_IMAGE_HOST)/$(AWS_IMAGE_REPO)/$(IMAGE):$(IMAGE_VERSION)$(IMAGE_REVISION)
	docker tag $(AWS_IMAGE_HOST)/$(AWS_IMAGE_REPO)/$(IMAGE):$(IMAGE_VERSION)$(IMAGE_REVISION) $(IMAGE):$(IMAGE_VERSION)$(IMAGE_REVISION)

push: build aws_login
	docker tag $(IMAGE):$(IMAGE_VERSION)$(IMAGE_REVISION) $(AWS_IMAGE_HOST)/$(AWS_IMAGE_REPO)/$(IMAGE):$(IMAGE_VERSION)$(IMAGE_REVISION)
	docker push $(AWS_IMAGE_HOST)/$(AWS_IMAGE_REPO)/$(IMAGE):$(IMAGE_VERSION)$(IMAGE_REVISION)

run:
	$(eval OM_HOST := $(shell scripts/get-local-ip.sh))
	@ echo "Running container with Ops Manager pointing to: $(OM_HOST)"
	docker run --name $(CONTAINER) -p 8080:8080 -e SKIP_OPS_MANAGER_REGISTRATION -e OM_HOST='$(OM_HOST)' -t $(IMAGE):$(IMAGE_VERSION)$(IMAGE_REVISION)

start:
	@ echo "Restarting the $(CONTAINER) container..."
	- docker start $(CONTAINER)

stop:
	@ echo "Stopping the $(CONTAINER) container..."
	- docker stop $(CONTAINER)
	@ while ! docker ps -a --filter 'status=exited' --filter 'status=paused' --filter 'status=dead' 2>/dev/null | grep -s 'ops_manager' 2>/dev/null; do sleep 3; done

restart:
	@ $(MAKE) stop start

status:
	@ echo "IP Address ($(CONTAINER)):"
	@ docker inspect -f "{{ .NetworkSettings.IPAddress }}" $(CONTAINER)
	@ echo "Running processes:"
	@ docker ps -a -f 'name=$(CONTAINER)'

wait:
	@ echo "Waiting for Ops Manager to start..."
	@ while ! curl -qsI http://127.0.0.1:8080/user | grep '200 OK' >/dev/null; do printf . ; sleep 10; done
	@ echo "Waiting for Ops Manager global owner registration..."
	@ while ! docker exec ops_manager test -f /opt/mongodb/mms/.ops-manager-env; do printf . ; sleep 10; done

get_om_env:
	- docker exec ops_manager cat /opt/mongodb/mms/.ops-manager-env

version:
	@ echo $(IMAGE_VERSION)
