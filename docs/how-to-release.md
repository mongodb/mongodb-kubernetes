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

## Build and Push images to "development" registry


Build the operator and database images and push them to the "development" registry (Amazon ECR).

``` bash
evg patch -p ops-manager-kubernetes -v push_images_to_development -t push_images_to_development -f
```

This will result in two tasks being executed in `evergreen` that will push the images to:

* **ECR**: 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev

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

## QA Plan

Perform a sanity check with your Kops cluster. This cluster should
have access to the ECR images, as they are all hosted in AWS.

We still don't have a good QA plan so you should do several tests
involving at least, the 3 different types of objects. Standalones,
ReplicaSets and ShardedCluster. Make sure you use PersistentVolumes
and try a few updates to the custom objects (like updating the mongod
version).

## Merge to Master

If everything is going well up to this point, don't forget to merge your PR!

## Publish public repo

Just run

    scripts/evergreen/update_public_repo

This will copy the contents of the `public` directory in the `10gen/ops-manager-kubernetes` into
the root of the `mongodb/mongodb-enterprise-kubernetes`, the public repo.

This script will also generate YAML files that can be used to install
the operator in clusters with no Helm installed. These yaml files will
be copied into the public repo, they will not exist in the private
repo, and they should not be checked into the private repo either. You
can generate the public repo, without commiting to it, with:

    GENERATE_ONLY=oui ./scripts/evergreen/update_public_repo some_temp_dir

Where `some_temp_dir` is a temporary directory you want to use to test
the output of your yaml files generation. `GENERATE_ONLY` is a
variable that, when set, will not try to commit and push these
changes.

## Tag This Release

Create a tag for this particular release with the format `release-<tag>`. Use something like the following command:

    git tag "release-$(grep release release.yaml | awk ' -F ":" { print $2 } ')"
    git push --tags

