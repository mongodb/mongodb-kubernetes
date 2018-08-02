# Operator Release

The Kubernetes Operator is based in 2 different images: Operator and Database images.
They follow a simple versioning schema (0.1, 0.2... 0.10... 1.0). The release
process is documented here:

## We don't have release branches (yet)

This means that the builds will come out from the `master` branch (this is
what we consider stable). Make sure every relevant PR has been merged, and
that you have the latest master locally.

## Pick up a new version

You will find the latest version in the `release.yaml` file in the root
directory of the project. Just increase the minor version in 1 unit:

```yaml
---
releaseTag: 0.1
```

Should be changed to:

```yaml
---
releaseTag: 0.2
```

## Update the version in several yaml files

This can be done running the following command:

```
scripts/evergreen/update_release_version.py
```

This will modify some files that are meant to be distributed in the
`public` repo.

## Push these changes to a PR

Create a new branch with the name of the release ticket and push your changes
there.

## Build and Push images to "development" and "staging" repos

There are 2 additional repos to where to push the image, called "development" and "staging".
To push the new image into this repo, do the following:

``` bash
evg patch -p ops-manager-kubernetes -v push_images_to_development -t push_images_to_development -f
evg patch -p ops-manager-kubernetes -v push_images_to_staging -t push_images_to_staging -f
```

This will result in two tasks being executed in `evergreen` that will push the images to:

* **ECR**: 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev
* **Private Quay**: quay.io/repository/mongodb-enterprise-private

## Build the images and push to Quay Public Docker repo

This should be done using `evergreen` with one of the provided tasks:

``` yaml
evg patch -p ops-manager-kubernetes -v release -t release
```

This will build the `mongodb-enterprise-operator` and
`mongodb-enterprise-database` images and push to the `quay.io` public
repo. The images will be tagged with whatever is on the `release.yaml` file.

**Caution**: The current tagged images will be overwritten by
`evergreen` if they have the same tag as any old images.

**TODO**: Have some kind of git-tagging?

## QA Plan

**TODO: Super Important**

## Merge to Master

If everything is going well up to this point, don't forget to merge your PR!

## Publish public repo

Just run

    scripts/evergreen/update_public_repo

This will copy the contents of the `public` directory in the `10gen/ops-manager-kubernetes` into
the root of the `mongodb/mongodb-enterprise-kubernetes`, the public repo.
