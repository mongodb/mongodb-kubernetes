IMAGE = dev/mongodb-enterprise-database

usage:
	@ echo "MongoDB Enterprise Database - Docker image builder"
	@ echo
	@ echo "Usage:"
	@ echo "  clean: 	      deletes the image and any running containers"
	@ echo "  build: 	      builds the Docker image"
	@ echo "  run: 		      runs the image in a new container"
	@ echo "  all: 		      shorthand for 'clean build run'"
	@ echo "  ps: 		      shows all the containers created using this image"
	@ echo


clean:
	- docker ps -a | tail -n +2 | grep '$(IMAGE)' | awk '{print $$1}' | xargs docker rm -f
	- docker rmi $(IMAGE)

build:
	docker build -t $(IMAGE) .

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
	docker run -e BASE_URL -e GROUP_ID -e AGENT_API_KEY -t $(IMAGE)

all:
	@ $(MAKE) clean build run

ps:
	@ docker ps -a | tail -n +2 | grep '$(IMAGE)'
