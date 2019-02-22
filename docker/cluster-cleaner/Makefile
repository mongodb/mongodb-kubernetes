build:
	docker build -t dev/cluster-cleaner:0.1 .

push:
	$(aws ecr get-login --no-include-email --region us-east-1)
	docker tag dev/cluster-cleaner:0.1 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cluster-cleaner:0.1
	docker push 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cluster-cleaner:0.1
