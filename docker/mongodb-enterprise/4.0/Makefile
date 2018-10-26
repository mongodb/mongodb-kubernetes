IMAGE = mongodb-enterprise
IMAGE_VERSION = latest
MONGODB_VERSION = 4.0.3
usage:
	@ echo "MongoDB Enterprise - Docker image builder"
	@ echo
	@ echo "Usage:"
	@ echo "  delete_container:	stops and deletes the container"
	@ echo "  clean:            deletes the image and any running containers"
	@ echo "  build:            builds the Docker image"
	@ echo "  clean-build:      rebuilds the Docker image from scratch (no-cache), stopping any running containers and deleting all intermediate containers"
	@ echo "  all:              shorthand for 'clean build run'"
	@ echo
	@ echo "  run:              runs the image in a new container"
	@ echo "  status:           shows all the containers created using this image"
	@ echo

delete_container:
	- docker ps -a | grep '$(IMAGE)' | awk '{print $$1}' | xargs docker rm -f

clean:
	- docker rmi $(IMAGE):$(IMAGE_VERSION)

build:
	docker build $(EXTRA_PARAM) -t $(IMAGE):$(IMAGE_VERSION) . --build-arg MONGO_VERSION=$(MONGODB_VERSION)

clean-build:
	@ $(MAKE) EXTRA_PARAM="--no-cache --force-rm --rm" clean build

run:
	docker run -t $(IMAGE):$(IMAGE_VERSION)



all:
	@ echo ""
	@ $(MAKE) clean
	@ echo ""
	@ $(MAKE) build
	@ echo ""
	@ $(MAKE) run

status:
	@ docker ps -a | head -1
	@ docker ps -a | tail -n +2 | grep '$(IMAGE)'
