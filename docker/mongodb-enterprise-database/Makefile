IMAGE = mongodb-enterprise-database
IMAGE_VERSION = latest
IMAGE_DIR = dev
AWS_IMAGE_REPO = 268558157000.dkr.ecr.us-east-1.amazonaws.com
AWS_REGION = us-east-1
IMAGE_TYPE = ubuntu

usage:
	@ echo "MongoDB Enterprise Database - Docker image builder"
	@ echo
	@ echo "Usage:"
	@ echo "  delete_container:	stops and deletes the container"
	@ echo "  clean: 	        deletes the image and any running containers"
	@ echo "  build: 	        builds the Docker image"
	@ echo "  clean-build:		rebuilds the Docker image from scratch (no-cache), stopping any running containers and deleting all intermediate containers"
	@ echo "  all: 		        shorthand for 'clean build run'"
	@ echo
	@ echo "  pull:			pulls the image from the configured Docker registry"
	@ echo "  push:			pushes the image to the configured Docker registry"
	@ echo
	@ echo "  run: 		        runs the image in a new container"
	@ echo "  status:	        shows all the containers created using this image"
	@ echo "  version:	        prints the image's version string (IMAGE_VERSION)"
	@ echo

delete_container:
	- docker ps -a | tail -n +2 | grep '$(IMAGE)' | awk '{print $$1}' | xargs docker rm -f

clean: delete_container
	- docker rmi $(IMAGE_DIR)/$(IMAGE):$(IMAGE_VERSION)-$(IMAGE_TYPE)

build:
	@ GOOS='linux' go build -i ../../probe/readiness.go
	@ mv readiness content/readinessprobe
	@ ../Dockerfile.gen "${IMAGE_TYPE}" > Dockerfile
	docker build $(EXTRA_PARAM) -t $(IMAGE_DIR)/$(IMAGE):$(IMAGE_VERSION)-$(IMAGE_TYPE) .

clean-build:
	@ $(MAKE) EXTRA_PARAM="--no-cache --force-rm --rm" clean build

aws_login:
	@ eval "$(shell aws ecr get-login --no-include-email --region $(AWS_REGION))"

pull: aws_login
	docker pull $(AWS_IMAGE_REPO)/$(IMAGE_DIR)/$(IMAGE):$(IMAGE_VERSION)-$(IMAGE_TYPE)
	docker tag $(AWS_IMAGE_REPO)/$(IMAGE_DIR)/$(IMAGE):$(IMAGE_VERSION)-$(IMAGE_TYPE) $(IMAGE_DIR)/$(IMAGE):$(IMAGE_VERSION)-$(IMAGE_TYPE)

push: build aws_login
	docker tag $(IMAGE_DIR)/$(IMAGE):$(IMAGE_VERSION)-$(IMAGE_TYPE) $(AWS_IMAGE_REPO)/$(IMAGE_DIR)/$(IMAGE):$(IMAGE_VERSION)-$(IMAGE_TYPE)
	docker push $(AWS_IMAGE_REPO)/$(IMAGE_DIR)/$(IMAGE):$(IMAGE_VERSION)-$(IMAGE_TYPE)

check-ops-manager-env:
ifndef BASE_URL
	$(error Environment variable BASE_URL is not set)
endif
ifndef GROUP_ID
	$(error Environment variable GROUP_ID is not set)
endif
ifndef AGENT_API_KEY
	$(error Environment variable AGENT_API_KEY is not set)
endif

run: check-ops-manager-env
	docker run -e BASE_URL -e GROUP_ID -e AGENT_API_KEY -e HTTP_PROXY -t  $(IMAGE):$(IMAGE_VERSION)-$(IMAGE_TYPE)

all:
	@ $(MAKE) clean build run

status:
	@ docker ps -a | head -1
	@ docker ps -a | tail -n +2 | grep '$(IMAGE)'

version:
	@ echo $(IMAGE_VERSION)
