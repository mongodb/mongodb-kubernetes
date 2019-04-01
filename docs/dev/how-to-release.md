# Operator Release

The Kubernetes Operator is composed of two different images: the Operator and
the Database image. They follow a simple versioning schema (0.1, 0.2... 0.10...
1.0). The release process is documented here:

## Update Jira to reflect the patches in the release (Tuesday)

Update any finished tickets in [kube-next](https://jira.mongodb.org/issues/?jql=project%20%3D%20CLOUDP%20AND%20component%20%3D%20Kubernetes%20AND%20status%20in%20(Resolved%2C%20Closed)%20and%20fixVersion%3D%20kube-next%20) to have the version of the release you're doing (kube-x.y)

## Prepare Release Notes (Tuesday)

Create a DOCSP ticket describing the tickets in the current [fixVersion](https://jira.mongodb.org/issues/?jql=project%20%3D%20CLOUDP%20AND%20component%20%3D%20Kubernetes%20AND%20status%20in%20(Resolved%2C%20Closed)%20and%20fixVersion%3D%20kube-next%20)

Ensure there is a link to our quay.io tags, and if there are any Medium or higher CSVs **include a section in the release notes**

```
A list of the packages installed, and any security vulnerabilities detected in our build process, are outlined here

For the MongoDB Enterprise Operator
https://quay.io/repository/mongodb/mongodb-enterprise-operator?tab=tags

And for the MongoDB Enterprise Database
https://quay.io/repository/mongodb/mongodb-enterprise-database?tab=tags
```

## Release ticket, branch and PR

* Create a branch named after release ticket.
* Check that all samples and `README` in `public` folder are up-to-date -
  otherwise fix them and push changes.

## Increase release versions in relevant files

Ensure the required dependencies are installed 
```bash
pip3 install -r scripts/evergreen/requirements.txt
```

```bash
./scripts/evergreen/update_release_version.py <version>
```
This will update all relevant files with a new version
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

Most of the standard operations with 3 MongoDB resources are covered by E2E tests, try to look 
through the tickets that got into the release and check if any additional QA is necessary. 

## Get the release PR approved and merge the branch to Master

Ask someone from the team to approve the PR.

Merge the release branch to master

## Tag the commit for release

Create a signed and annotated tag for this particular release. Set the message contents to the release notes.

```bash
git tag --annotate --sign $(jq --raw-output .mongodbOperator < release.json)
git push origin $(jq --raw-output .mongodbOperator < release.json)
```

## Build and push images

### Debian based images (published to Quay)

After successful QA publish images to the public repo.

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

* https://connect.redhat.com/project/850021/build-service
* https://connect.redhat.com/project/851701/build-service

Note, that so far the build service is not reliable and some checks may fail - you need to trigger new builds on the
site manually specifying the new version (increase the patch part, e.g. `0.7.1`) and hopefully the build will succeed
eventually.

Finally publish the images manually:
* https://connect.redhat.com/project/850021/view (Operator)
* https://connect.redhat.com/project/851701/view (Database)

(more details about RHEL build process are in https://github.com/10gen/kubernetes-rhel-images)

## Publish public repo

Just run

    scripts/evergreen/update_public_repo <path_to_public_repo_root>

This will copy the contents of the `public` directory in the `10gen/ops-manager-kubernetes` into
the root of the `mongodb/mongodb-enterprise-kubernetes`, the public repo and will commit changes (not push!)

This script will also generate YAML files that can be used to install
the operator in clusters with no Helm installed. These yaml files will
be copied into the public repo, they will not exist in the private
repo, and they should not be checked into the private repo either. 

Check the last commit in the public repo and if everything is ok - push it.

## Create Release Notes on Github
Copy the Release Notes from the DOCSP [into Github](https://github.com/mongodb/mongodb-enterprise-kubernetes/releases/new)

## Release in Github

Publish release in our public Github repository
[https://github.com/mongodb/mongodb-enterprise-kubernetes/releases](https://github.com/mongodb/mongodb-enterprise-kubernetes/releases)

## Create the next release ticket

    make_jira_tickets kube_release 0.9

## Release in Jira

Ask someone with permission (Crystal/James/David/Rahul ) to "release" the version in Jira and create next ones
