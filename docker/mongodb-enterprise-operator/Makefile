IMAGE = mongodb-enterprise-operator
IMAGE_VERSION = latest
IMAGE_DIR = dev
AWS_IMAGE_REPO = 268558157000.dkr.ecr.us-east-1.amazonaws.com
AWS_REGION = us-east-1
IMAGE_TYPE = ubuntu
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
	- docker rmi $(IMAGE_DIR)/$(IMAGE):$(IMAGE_VERSION)-$(IMAGE_TYPE)

build:
	@ ../Dockerfile.gen "${IMAGE_TYPE}" False $(IS_DEBUG) > Dockerfile
	@ DEBUG=$(IS_DEBUG) ../../scripts/build/build_operator || (../../scripts/build/prepare_build_environment && ../../scripts/build/build_operator)
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

version:
	@ echo $(IMAGE_VERSION)-$(IMAGE_TYPE)
