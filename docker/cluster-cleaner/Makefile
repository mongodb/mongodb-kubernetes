IMAGE_VERSION=0.5

build:
	docker build -t dev/cluster-cleaner:$(IMAGE_VERSION) .

push:
	$(aws ecr get-login --no-include-email --region us-east-1)
	docker tag dev/cluster-cleaner:$(IMAGE_VERSION) 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cluster-cleaner:$(IMAGE_VERSION)
	docker push 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cluster-cleaner:$(IMAGE_VERSION)

install:
	kubectl create namespace cluster-cleaner || true
	helm template . \
		--set cleanerVersion=$(IMAGE_VERSION) \
		--set namespace=cluster-cleaner\
		--set cleanerNamespace=cluster-cleaner > cluster-cleaner.yaml
	kubectl apply -f cluster-cleaner.yaml
	rm cluster-cleaner.yaml
