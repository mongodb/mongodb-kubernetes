IMAGE = mongodb-enterprise-operator
IMAGE_VERSION = 0.1
AWS_IMAGE_REPO = 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev
AWS_REGION = us-east-1

usage:
	@ echo "MongoDB Enterprise Kubernetes Operator - Docker image builder"
	@ echo "NOTE: This Dockerfile can only be used in a Kubernetes cluster!"
	@ echo
	@ echo "Usage:"
	@ echo "  delete_container:	stops and deletes the container"
	@ echo "  clean: 	        deletes the image and any running containers"
	@ echo "  build: 	        builds the Docker image"
	@ echo "  clean-build:      rebuilds the Docker image from scratch (no-cache), stopping any running containers and deleting all intermediate containers"
	@ echo
	@ echo "  pull: 		    pulls the image from the configured Docker registry"
	@ echo "  push: 		    pushes the image to the configured Docker registry"
	@ echo
	@ echo "  version:	        prints the image's version string (IMAGE_VERSION)"
	@ echo


delete_container:
	- docker ps -a | tail -n +2 | grep '$(IMAGE)' | awk '{print $$1}' | xargs docker rm -f

clean: delete_container
	- docker rmi $(IMAGE):$(IMAGE_VERSION)

build:
	../../scripts/build/build_operator
	docker build $(EXTRA_PARAM) -t $(IMAGE):$(IMAGE_VERSION) .

clean-build:
	@ $(MAKE) EXTRA_PARAM="--no-cache --force-rm --rm" clean build

aws_login:
	@ eval "$(shell aws ecr get-login --no-include-email --region $(AWS_REGION))"

pull: aws_login
	docker pull $(AWS_IMAGE_REPO)/$(IMAGE):$(IMAGE_VERSION)
	docker tag $(AWS_IMAGE_REPO)/$(IMAGE):$(IMAGE_VERSION) $(IMAGE):$(IMAGE_VERSION)

push: build aws_login
	docker tag $(IMAGE):$(IMAGE_VERSION) $(AWS_IMAGE_REPO)/$(IMAGE):$(IMAGE_VERSION)
	docker push $(AWS_IMAGE_REPO)/$(IMAGE):$(IMAGE_VERSION)

version:
	@ echo $(IMAGE_VERSION)
