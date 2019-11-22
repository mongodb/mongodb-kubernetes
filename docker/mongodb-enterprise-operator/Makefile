IMAGE = mongodb-enterprise-operator
IMAGE_VERSION = latest
IMAGE_DIR = dev
AWS_IMAGE_REPO = 268558157000.dkr.ecr.us-east-1.amazonaws.com
AWS_REGION = us-east-1
IMAGE_TYPE = ubuntu
IMAGE_PATH = $(IMAGE_DIR)/$(IMAGE_TYPE)/$(IMAGE):$(IMAGE_VERSION)
IS_DEBUG = False

usage:
	@ echo "MongoDB Enterprise Kubernetes Operator - Docker image builder"
	@ echo "NOTE: This Dockerfile can only be used in a Kubernetes cluster!"
	@ echo
	@ echo "Usage:"
	@ echo "  delete_container:     stops deletes the container"
	@ echo "  clean:                deletes the image and any running containers"
	@ echo "  build:                builds the Docker image"
	@ echo "  clean-build:          rebuilds the Docker image from scratch (no-cache), stopping any running containers and deleting all intermediate containers"
	@ echo
	@ echo "  pull:                 pulls the image from the configured Docker registry"
	@ echo "  push:                 pushes the image to the configured Docker registry"
	@ echo
	@ echo "  version:              prints the image's version string (IMAGE_VERSION)"
	@ echo


delete_container:
	- docker ps -a | tail -n +2 | grep '$(IMAGE)' | awk '{print $$1}' | xargs docker rm -f

clean: delete_container
	- docker rmi $(IMAGE_PATH)

build:
	@ test $(IS_DEBUG) = "True" && DEBUG_FLAG="--debug" ; ../Dockerfile.gen operator $(IMAGE_TYPE) $$DEBUG_FLAG > Dockerfile
	@ DEBUG=$(IS_DEBUG) ../../scripts/build/build_operator || \
		(../../scripts/build/prepare_build_environment && ../../scripts/build/build_operator)
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

version:
	@ echo $(IMAGE_VERSION)
