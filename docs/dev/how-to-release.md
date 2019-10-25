# Operator Release

The Kubernetes Operator is composed of two different images: the Operator and
the Database image. They follow a simple versioning schema (0.1, 0.2... 0.10...
1.0). The release process is documented here:

## Update Jira to reflect the patches in the release (Tuesday)

Update any finished tickets in [kube-next](https://jira.mongodb.org/issues/?jql=project%20%3D%20CLOUDP%20AND%20component%20%3D%20Kubernetes%20AND%20status%20in%20(Resolved%2C%20Closed)%20and%20fixVersion%3D%20kube-next%20) to have the version of the release you're doing (kube-x.y)

## Prepare Release Notes (Tuesday)

Draft the release notes describing the tickets in the current
[fixVersion](https://jira.mongodb.org/issues/?jql=project%20%3D%20CLOUDP%20AND%20component%20%3D%20Kubernetes%20AND%20status%20in%20(Resolved%2C%20Closed)%20and%20fixVersion%3D%20kube-next%20).

Submit the proposed release notes in the team Slack channel
([#k8s-operator-devs](https://mongodb.slack.com/messages/CGLP6R2PQ)) for peer
review. Once people have had a chance to look at them, create a DOCSP ticket to
publish the release notes. If the DOCSP ticket has not been assigned by
Wednesday, ask about it in the #docs channel.

Ensure there is a link to our quay.io tags, and if there are any Medium or higher CVEs **include a section in the release notes**.

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

## Get the release PR approved and merge the branch to Master

Ask someone from the team to approve the PR.

Merge the release branch to master

## Tag the commit for release

Checkout the latest master and pull changes
Create a signed and annotated tag for this particular release. Set the message contents to the release notes.

```bash
git checkout master
git pull
git tag --annotate --sign $(jq --raw-output .mongodbOperator < release.json)
git push origin $(jq --raw-output .mongodbOperator < release.json)
```

## Build and push images

### Ubuntu based images (published to Quay)

```bash
evergreen patch -p ops-manager-kubernetes -v release_operator -t release_operator -y -f -d "Building images for release $(jq -r .mongodbOperator < release.json)" --browse

```

This evergreen task will build the images and publish them to `quay.io`.

**Caution**: quay.io doesn't allow to block tags from overwriting, the
current tagged images will be overwritten by `evergreen` if they have
the same tag as any old images.

### RHEL based images (published to Red Hat Connect)

*Please note:* This is going to be improved as part of
[CLOUDP-40403](https://jira.mongodb.org/browse/CLOUDP-40403).

For RHEL based images we use Red Hat Connect service that will build
images by itself eventually.  To submit the job call the following:

```bash
evergreen patch -p ops-manager-kubernetes -v release_operator_rhel -t release_operator_rhel_connect -f
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

## Ask the Docs team to publish the Release Notes
Do this in the #docs channel

## Create Release Notes on Github
Copy the Release Notes from the DOCSP [into Github](https://github.com/mongodb/mongodb-enterprise-kubernetes/releases/new)

## Release in Github

Publish release in our public Github repository
[https://github.com/mongodb/mongodb-enterprise-kubernetes/releases](https://github.com/mongodb/mongodb-enterprise-kubernetes/releases)

## Update Operator in Kanopy

Create a
[ticket](https://jira.mongodb.org/projects/TECHOPS/welcome-guide) for
TechOps to update their Kanopy Kubernetes cluster to latest release of
our Operator.

## Create the next release ticket

    make_jira_tickets kube_release 0.9

## Release in Jira

Ask someone with permission (Crystal/James/David/Rahul ) to "release" the version in Jira and create next ones

## Publish the New Version into Operatorhub.io and Openshift Marketplace

Find instructions [here](publishing-to-marketplaces.md).

## Inform TechOps
Let Ricardo (ricardo.hernandez) know that we've released a new version and link
him the release notes. This will ensure that the Operator as deployed in Kanopy
is updated appropriately. Also offer to remove this notification from our
release process if it proves unnecessary.
