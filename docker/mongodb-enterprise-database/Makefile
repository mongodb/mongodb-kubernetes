IMAGE = mongodb-enterprise-database
IMAGE_APPDB = mongodb-enterprise-appdb
IMAGE_VERSION = latest
IMAGE_DIR = dev
AWS_IMAGE_REPO = 268558157000.dkr.ecr.us-east-1.amazonaws.com
AWS_REGION = us-east-1
IMAGE_TYPE = ubuntu
AA_DOWNLOAD_LOCATION = https://s3.amazonaws.com/mciuploads/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod
AA_VERSION = 10.2.2.5862-1
TEST_AUTOMATION_CONFIG = test/replset.json
IMAGE_PATH = $(IMAGE_DIR)/$(IMAGE_TYPE)/$(IMAGE):$(IMAGE_VERSION)
# Image suffix allows to separate different build versions. The main planned sample is '-operatorX.Y.Z'
IMAGE_SUFFIX =

usage:
	@ echo "MongoDB Enterprise Database - Docker image builder"
	@ echo
	@ echo "Usage:"
	@ echo "  (Database)"
	@ echo "  delete_container:	      stops and deletes the container"
	@ echo "  clean: 	              deletes the image and any running containers"
	@ echo "  build: 	              builds the Docker image"
	@ echo "  clean-build:		      rebuilds the Docker image from scratch (no-cache), stopping any running containers and deleting all intermediate containers"
	@ echo "  all: 		              shorthand for 'clean build run'"
	@ echo
	@ echo "  pull:			      pulls the image from the configured Docker registry"
	@ echo "  push:			      pushes the image to the configured Docker registry"
	@ echo
	@ echo "  run: 		              runs the image in a new container"
	@ echo "  status:	              shows all the containers created using this image"
	@ echo "  version:	              prints the image's version string (IMAGE_VERSION)"

delete_container:
	- docker ps -a | tail -n +2 | grep '$(IMAGE)' | awk '{print $$1}' | xargs docker rm -f

clean: delete_container
	- docker rmi $(IMAGE_PATH)

prepare-build:
	@ GOOS='linux' go build -i -o readiness ../../probe
	@ mv readiness content/readinessprobe
	@ ../Dockerfile.gen database "$(IMAGE_TYPE)" > Dockerfile

build: prepare-build
	docker build $(EXTRA_PARAM) -t $(IMAGE_PATH) .

clean-build:
	@ $(MAKE) EXTRA_PARAM="--no-cache --force-rm --rm" clean build

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
	docker run -e BASE_URL -e GROUP_ID -e AGENT_API_KEY -e HTTP_PROXY -t  $(IMAGE_PATH)

all:
	@ $(MAKE) clean build run

status:
	@ docker ps -a | head -1
	@ docker ps -a | tail -n +2 | grep '$(IMAGE)'

version:
	@ echo $(IMAGE_VERSION)

