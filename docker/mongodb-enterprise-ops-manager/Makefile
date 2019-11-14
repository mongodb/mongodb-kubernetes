IMAGE = mongodb-enterprise-ops-manager
# Image version can be picked from the download url - just remove "mongodb-mms_" prefix and "_x86_64.deb" suffix
IMAGE_VERSION=4.2.2
IMAGE_DIR = dev
CONTAINER = ops_manager
AWS_IMAGE_REPO = 268558157000.dkr.ecr.us-east-1.amazonaws.com
# AWS_IMAGE_REPO =
AWS_REGION = us-east-1
OM_DOWNLOAD_LINK = https://downloads.mongodb.com/on-prem-mms/tar/mongodb-mms-4.2.0.56551.20190812T1752Z-1.x86_64.tar.gz
APPDB_PORT = 27017
IP = $(shell ifconfig | grep -Eo 'inet (addr:)?([0-9]*\.){3}[0-9]*' | grep -Eo '([0-9]*\.){3}[0-9]*' | grep -v '127.0.0.1' | head -1)
IMAGE_TYPE = ubuntu
IMAGE_PATH = $(IMAGE_DIR)/$(IMAGE_TYPE)/$(IMAGE):$(IMAGE_VERSION)

usage:
	@ echo "MongoDB Enterprise Ops Manager - Docker image builder"
	@ echo
	@ echo "Usage:"
	@ echo "  delete_container:      stops and deletes the container"
	@ echo "  clean:                 deletes the image and any running containers"
	@ echo "  build:                 builds the Docker image"
	@ echo "  clean_build:           rebuilds the Docker image from scratch (no-cache), stopping any running containers and deleting all intermediate containers"
	@ echo "  all:                   shorthand for 'clean build run'"
	@ echo "  pull:                  pulls the image from the configured Docker registry"
	@ echo "  push:                  pushes the image to the configured Docker registry"
	@ echo
	@ echo "  run:                   runs the image in a new container. Note, that you must start mongo database on port"
	@ echo "                         'APPDB_PORT' (default 27017) beforehand"
	@ echo "  start:                 (re)starts an existing container"
	@ echo "  stop:                  stops a running container, if one exists"
	@ echo "  restart:               stops any running containers, then restarts"
	@ echo "  status:                shows the running container's IP and processes"
	@ echo
	@ echo "  wait:                  wait until Ops Manager has started and was configured (Docker only, not Kubernetes)"
	@ echo "  version:               prints the image's version string (IMAGE_VERSION)"
	@ echo

delete_container:
	@ docker rm -f $(CONTAINER) || true

clean: delete_container
	@ docker rmi $(IMAGE_PATH) || true

build:
	docker build $(EXTRA_PARAM) \
		-t $(IMAGE_PATH) \
		--build-arg OM_DOWNLOAD_LINK=$(OM_DOWNLOAD_LINK) \
		.

clean_build:
	@ rm -rf cache/*
	@ $(MAKE) EXTRA_PARAM="--no-cache --force-rm --rm" clean build

all:
	@ $(MAKE) clean build run

# dev note on '&> /dev/null || true': if the 'aws_login' is run in parallel (e.g. 'make' launches builds for images
# in parallel and both call 'aws_login') then Docker login may return an error "Error saving credentials:..The
# specified item already exists in the keychain". Seems this allows to ignore the error
aws_login:
	@ eval "$(shell aws ecr get-login --no-include-email --region $(AWS_REGION))" &> /dev/null || true

pull: aws_login
	docker pull $(AWS_IMAGE_REPO)/$(IMAGE_PATH)
	docker tag $(AWS_IMAGE_REPO)/$(IMAGE_PATH) $(IMAGE_PATH)

push: build aws_login
	docker tag $(IMAGE_PATH) $(AWS_IMAGE_REPO)/$(IMAGE_PATH)
	docker push $(AWS_IMAGE_REPO)/$(IMAGE_PATH)

run:
	@ echo "Make sure you run the mongodb on port $(APPDB_PORT)"
	docker run --name $(CONTAINER) -p 8080:8080 -e OM_PROP_mongo_mongoUri='mongodb://$(IP):$(APPDB_PORT)' -t $(IMAGE_PATH)-

start:
	@ echo "Restarting the $(CONTAINER) container..."
	- docker start $(CONTAINER)

stop:
	@ echo "Stopping the $(CONTAINER) container..."
	- docker stop $(CONTAINER)
	@ while ! docker ps -a --filter 'status=exited' --filter 'status=paused' --filter 'status=dead' 2>/dev/null | grep -s 'ops_manager' 2>/dev/null; do sleep 3; done

restart:
	@ $(MAKE) stop start

version:
	@ echo $(IMAGE_VERSION)
