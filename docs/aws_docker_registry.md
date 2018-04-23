## Using the ECR (Amazon Container Registry)

*Due to Docker approach "repository = application" the only way to isolate different namespaces is to inject them into names of application. So instead of `om-operator` the image and the repository will get the name `alisovenko/om-operator`.* 
### Integrate Docker with registry

We have the ECR registry ready for use: `268558157000.dkr.ecr.us-east-1.amazonaws.com`. To get access to it through Docker you need to use `docker login` command. Run the following command (more information [here](https://docs.aws.amazon.com/AmazonECR/latest/userguide/Registries.html#registry_auth)):

```bash
eval $(aws ecr get-login --no-include-email --region us-east-1) # aws ecr get-login creates the text for 'docker login' command
```
This will allow to use `docker push` to publish changes 

### Create new repository

```bash
aws ecr create-repository --repository-name dev2/om-operator
```

This will create a new repository named `dev2/om-operator`. You can delete it easily using `aws ecr` commands


### Push the build to ECR repository

1. Build the image locally

```bash
docker build -t dev2/om-operator:latest .
```

2. Tag it

```bash
docker tag dev2/om-operator:latest 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev2/om-operator:latest
```

3. Push

```bash
docker push 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev2/om-operator:latest 
```

It's possible to push the same image again with different tag
