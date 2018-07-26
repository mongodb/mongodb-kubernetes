## Using the ECR (Amazon Container Registry)

*Due to Docker approach "repository = application" the only way to isolate different namespaces is to inject them into names of application. 
So instead of `mongodb-enterprise-operator` the image and the repository will get the name `dev/mongodb-enterprise-operator` or `alis/mongodb-enterprise-operator`.* 


### Integrate Docker with registry

We have the ECR registry ready for use: `268558157000.dkr.ecr.us-east-1.amazonaws.com`. To get access to it through Docker you need to use `docker login` command. Run the following command (more information [here](https://docs.aws.amazon.com/AmazonECR/latest/userguide/Registries.html#registry_auth)):

```bash
eval $(aws ecr get-login --no-include-email --region us-east-1) # aws ecr get-login creates the text for 'docker login' command
```
This will allow to use `docker push` to publish changes 

Note the AWS account id is: `2685-5815-7000`


### Create new repository

```bash
aws ecr create-repository --repository-name dev/mongodb-enterprise-operator
```

This will create a new repository named `dev/mongodb-enterprise-operator`. You can delete it easily using `aws ecr` commands


### Push the build to ECR repository

Build the image locally, tag it and push:

(for operator)

```bash
docker build -t dev/mongodb-enterprise-operator . &&
docker tag dev/mongodb-enterprise-operator 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-operator &&
docker push 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-operator 
```

(for automation agent)

```bash
cd docker/database/ &&
docker build -t dev/mongodb-enterprise-database . &&
docker tag dev/mongodb-enterprise-database 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-database &&
docker push 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-database 
```
