build:
	docker build -t dev/cluster-cleaner:0.4 .

push:
	$(aws ecr get-login --no-include-email --region us-east-1)
	docker tag dev/cluster-cleaner:0.4 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cluster-cleaner:0.4
	docker push 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cluster-cleaner:0.4

install:
	kubectl create namespace cluster-cleaner || true
	helm template . \
		--set namespace=cluster-cleaner\
		--set cleanerNamespace=cluster-cleaner > cluster-cleaner.yaml
	kubectl apply -f cluster-cleaner.yaml
	rm cluster-cleaner.yaml
