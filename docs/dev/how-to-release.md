# Operator Release

The Kubernetes Operator is composed of two different images: the Operator and
the Database image. They follow a simple versioning schema (0.1, 0.2... 0.10...
1.0). The release process is documented here:

## Prepare the release notes in public Github repository (one day before)

Describe the release notes using link
[https://github.com/mongodb/mongodb-enterprise-kubernetes/releases/new](https://github.com/mongodb/mongodb-enterprise-kubernetes/releases/new)
save as draft.

You can find all resolved tickets ordered by resolve date using the following
[filter](https://jira.mongodb.org/issues/?filter=26728) (put the correct
`fixRelease` if it's not set).

Create a DOCSP ticket with the same content (ideally this should be done at
least one day before the real release) and link it to the release ticket.

## Release ticket, branch and PR

* Create a release ticket.
* Make sure every relevant PR has been merged to master, and that you have the
  latest version locally.
* Create a branch named after release ticket.
* Check that all samples and `README` in `public` folder are up-to-date -
  otherwise fix them and push changes.

## Increase release versions in relevant files

Update the version in `release.json` appropriately. For instance, the following
release manifest:

```json
{
  "mongodbOperator": "0.5",
  "mongodbEnterprise": "4.0.3",
  "automation.agent.version": "5.4.7.5469-1"
}
```

could be changed to:

```json
{
  "mongodbOperator": "0.6",
  "mongodbEnterprise": "4.0.3",
  "automation.agent.version": "5.4.7.5469-1"
}
```

Increase the version in all other files based on `release.json` one (`todo:
teach update_release_version.py file update release.json as well`):

```
scripts/evergreen/update_release_version.py
```

This will modify some files that are meant to be distributed in the
`public` repo.

Push the PR changes

## QA

Ensure that your branch is up to date with master.

Build the operator and database images and push them to the "development"
registry (Amazon ECR).

``` bash
evergreen patch -p ops-manager-kubernetes -v build_and_push_images_development -f
```

Perform a sanity check with your Kops cluster using just built images. This
cluster should have access to the ECR images, as they are all hosted in AWS.

We still don't have a good QA plan so you should do several tests involving at
least the 3 different types of custom resource: Standalones, ReplicaSets and
ShardedClusters. Make sure you use PersistentVolumes and try a few updates to
the custom objects (like updating the mongod version).

## Get the release PR approved and merge the branch to Master

Ask someone from the team to approve the PR.

Merge the release branch to master

## Tag the commit for release

Create a signed and annotated tag for this particular release. Use the same
semantic version as you earlier defined in the release.json file for the tag
name. Set the message contents to the release notes.

```bash
git tag --annotate --sign $(jq --raw-output .mongodbOperator < release.json)
git push origin $(jq --raw-output .mongodbOperator < release.json)
```

## Build and push images

### Debian based images (published to Quay)

After successful QA publish images to public repo.

This should be done using `evergreen`:

```bash
evergreen patch -p ops-manager-kubernetes -v release_operator -f
```

This will build the `mongodb-enterprise-operator` and
`mongodb-enterprise-database` images and push to the `quay.io` public
repo. The images will be tagged with whatever is on the `release.json` file.

**Caution**: quay.io doesn't allow to block tags from overwriting, the current tagged images will be overwritten by
`evergreen` if they have the same tag as any old images.

### RHEL based images (published to Red Hat Connect)

For RHEL based images we use Red Hat Connect service that will build images by itself eventually.
To submit the job call the following:

```bash
evergreen patch -p ops-manager-kubernetes -v release_operator_rhel -f
```

Track the status of the jobs for operator and database using the following links:

https://connect.redhat.com/project/850021/build-service
https://connect.redhat.com/project/851701/build-service

Note, that so far the build service is not reliable and some checks may fail - you need to trigger new builds on the 
site manually specifying the new version (increase the patch part, e.g. `0.7.1`) and hopefully the build will succeed
eventually.

(more details about RHEL build process are in https://github.com/10gen/kubernetes-rhel-images)

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
the output of your yaml files generation.

## Release in Github

Publish release in our public Github repository
[https://github.com/mongodb/mongodb-enterprise-kubernetes/releases](https://github.com/mongodb/mongodb-enterprise-kubernetes/releases)

## Release in Jira

Ask your manager (Crystal/James) to "release" the version in Jira and create next ones

