Build enterprise images with

```bash
docker build -f path/to/dockerfile --build-arg MONGO_PACKAGE=mongodb-enterprise --build-arg MONGO_REPO=repo.mongodb.com --build-arg MONGO_VERSION=4.0.20 .
```

within the given directory.

