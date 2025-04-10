IMAGE_VERSION=0.14

.PHONY: all
all: build push install

build:
	docker build --platform linux/amd64 -t dev/cluster-cleaner:$(IMAGE_VERSION) .

push:
	$(aws ecr get-login --no-include-email --region us-east-1 || true)
	docker tag dev/cluster-cleaner:$(IMAGE_VERSION) 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cluster-cleaner:$(IMAGE_VERSION)
	docker tag dev/cluster-cleaner:$(IMAGE_VERSION) 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cluster-cleaner:latest

	docker push 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cluster-cleaner:$(IMAGE_VERSION)
	docker push 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cluster-cleaner:latest

install: build push
	kubectl create namespace cluster-cleaner || true
	helm template . \
		--set cleanerVersion=$(IMAGE_VERSION) \
		--set namespace=cluster-cleaner\
		--set cleanerNamespace=cluster-cleaner > cluster-cleaner.yaml
	kubectl apply -f cluster-cleaner.yaml
	rm cluster-cleaner.yaml
