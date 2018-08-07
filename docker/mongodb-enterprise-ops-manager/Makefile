IMAGE = mongodb-enterprise-ops-manager
IMAGE_VERSION = 4.0.0.49984.20180625T1427Z-1
CONTAINER = ops_manager
MONGODB_VERSION = 4.0.0
AWS_IMAGE_HOST = 268558157000.dkr.ecr.us-east-1.amazonaws.com
AWS_IMAGE_REPO = dev
AWS_REGION = us-east-1

usage:
	@ echo "MongoDB Enterprise Ops Manager - Docker image builder"
	@ echo
	@ echo "Usage:"
	@ echo "  delete_container:	stops and deletes the container"
	@ echo "  clean: 	      	deletes the image and any running containers"
	@ echo "  build: 	      	builds the Docker image"
	@ echo "  clean-build:      rebuilds the Docker image from scratch (no-cache), stopping any running containers and deleting all intermediate containers"
	@ echo "  all: 		      	shorthand for 'clean build run'"
	@ echo
	@ echo "  pull: 		  	pulls the image from the configured Docker registry"
	@ echo "  push: 		  	pushes the image to the configured Docker registry"
	@ echo
	@ echo "  run: 		      	runs the image in a new container"
	@ echo "  start: 	      	(re)starts an existing container"
	@ echo "  stop: 	      	stops a running container, if one exists"
	@ echo "  restart:	      	stops any running containers, then restarts"
	@ echo "  status:	      	shows the running container's IP and processes"
	@ echo
	@ echo "  wait: 		  	wait until Ops Manager has started and was configured (Docker only, not Kubernetes)"
	@ echo "  get_om_env:	  	prints the Ops Manager ENV variables for connecting to the running container (Docker only, not Kubernetes)"
	@ echo "  version:	      prints the image's version string (IMAGE_VERSION)"
	@ echo

delete_container:
	- docker rm -f $(CONTAINER)

clean: delete_container
	- docker rmi $(IMAGE):$(IMAGE_VERSION)

cache_binaries:
	@ mkdir -p cache
	@ [ -f "cache/mongodb-mms_${IMAGE_VERSION}_x86_64.deb" ] || curl -o "cache/mongodb-mms_${IMAGE_VERSION}_x86_64.deb" https://downloads.mongodb.com/on-prem-mms/deb/mongodb-mms_${IMAGE_VERSION}_x86_64.deb?utm_source=docker
	@ [ -f "cache/mongodb-linux-x86_64-debian92-${MONGODB_VERSION}.tgz" ] || curl -o "cache/mongodb-linux-x86_64-debian92-${MONGODB_VERSION}.tgz" https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-debian92-${MONGODB_VERSION}.tgz?utm_source=docker

build: cache_binaries
	docker build $(EXTRA_PARAM) \
		-t $(IMAGE):$(IMAGE_VERSION) \
		--build-arg MONGODB_VERSION=$(MONGODB_VERSION) \
		--build-arg OPS_MANAGER_VERSION=$(IMAGE_VERSION) \
		.

clean-build:
	@ rm -rf cache/*
	@ $(MAKE) EXTRA_PARAM="--no-cache --force-rm --rm" clean build

all:
	@ $(MAKE) clean build run

aws_login:
	@ eval "$(shell aws ecr get-login --no-include-email --region $(AWS_REGION))"

pull: aws_login
	docker pull $(AWS_IMAGE_HOST)/$(AWS_IMAGE_REPO)/$(IMAGE):$(IMAGE_VERSION)
	docker tag $(AWS_IMAGE_HOST)/$(AWS_IMAGE_REPO)/$(IMAGE):$(IMAGE_VERSION) $(IMAGE):$(IMAGE_VERSION)

push: build aws_login
	docker tag $(IMAGE):$(IMAGE_VERSION) $(AWS_IMAGE_HOST)/$(AWS_IMAGE_REPO)/$(IMAGE):$(IMAGE_VERSION)
	docker push $(AWS_IMAGE_HOST)/$(AWS_IMAGE_REPO)/$(IMAGE):$(IMAGE_VERSION)

run:
	$(eval OM_HOST := $(shell scripts/get-local-ip.sh))
	@ echo "Running container with Ops Manager pointing to: $(OM_HOST)"
	docker run --name $(CONTAINER) -p 8080:8080 -e SKIP_OPS_MANAGER_REGISTRATION -e OM_HOST='$(OM_HOST)' -t $(IMAGE):$(IMAGE_VERSION)

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
