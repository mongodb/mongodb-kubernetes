# Operator Release

The release procedure means building all necessary images and pushing them to
image repositories. Also all relevant places are upgraded (Openshift
Marketplace, public Github repository) and release notes are published.

The images released are:
- operator
- database
- ops manager
- appdb

## 1. Update Jira to reflect the patches in the release

Update any finished tickets in
[kube-enterprise-next](https://tinyurl.com/kube-next-resolved) to have the
version of the release you're doing (kube-x.y):

* Click `tools` -> `bulk change` in the top right corner
* Select all tickets and click `Next`
* Choose `Edit issues`
* Choose `Change Fix Version/s` -> `Replace all with` and select the released
  version, `Next`

## 2. Prepare Release Notes

Draft the release notes describing the tickets in the current
[fixVersion](https://tinyurl.com/kube-next).

Submit the proposed release notes in the team Slack channel
([#k8s-operator-devs](https://mongodb.slack.com/messages/CGLP6R2PQ)) for peer
review. Once people have had a chance to look at them, create a DOCSP ticket to
publish the release notes. If the DOCSP ticket has not been assigned by
Wednesday, ask about it in the #docs channel.

Ensure there is a link to our quay.io tags, and if there are any Medium or
higher CVEs **include a section in the release notes**.

```
A list of the packages installed, and any security vulnerabilities detected in our build process, are outlined here

For the MongoDB Enterprise Operator
https://quay.io/repository/mongodb/mongodb-enterprise-operator?tab=tags

And for the MongoDB Enterprise Database
https://quay.io/repository/mongodb/mongodb-enterprise-database?tab=tags
```

If this is a major-version release, include the EOL date in the Release Notes so
that the docs team update our [Support Lifecycle
page](https://docs.mongodb.com/kubernetes-operator/master/reference/support-lifecycle/#k8s-support-lifecycle)


## 3. Release ticket, branch and PR

* Create a branch named after release ticket.
* Check that all samples and `README` in `public` folder are up-to-date -
  otherwise fix them and push changes.

## 4. Increase release versions in relevant files

Ensure the required dependencies are installed
```bash
pip3 install -r scripts/evergreen/requirements.txt
```

Run the script in an interactive mode and fill the details for the versions of
the images to be released. Note, that Operator is always released but "init"
images are released only if there were changes in the content since the last
release. The script will check this and will ask for new versions if necessary.

```bash
git fetch
./scripts/evergreen/release/update_release_version.py
```

Push the PR changes

## 5. Get the release PR approved and merge the branch to Master

Ask someone from the team to approve the PR and then merge the release branch to
master.

## 6. Tag the commit for release

1. Checkout the latest master and pull changes
2. Create a signed and annotated tag for this particular release. Set the message contents to the release notes.

```bash
git checkout master
git pull
git tag --annotate --sign $(jq --raw-output .mongodbOperator < release.json)
git push origin $(jq --raw-output .mongodbOperator < release.json)
```

## 7. Build and push images

The following images are expected to get released by the end of this procedure:
* Operator
* Init Database
* Init Ops Manager
* Init AppDB

*(Database, AppDB and Ops Manager images are released manually)*

To perform release it's necessary to manually override dependencies in the tasks in the following
Evergreen build variants (after the release branch was merged):
* `release_quay` (will deploy all images to quay.io based on UBI and Ubuntu)
* `release_rh` (will deploy all images to Red Hat Connect)

**Note** that `appdb` release tasks should be run only if the new version of appdb image has been released

**Caution (quay.io)**: quay.io doesn't allow to block tags from overwriting, the
current tagged images will be overwritten by `evergreen` if they have
the same tag as any old images.

**Caution (Red Hat Connect)**: so far the build service is not reliable and some checks may fail - you need to trigger new builds on the
site manually specifying the new version (increase the patch part, e.g. `0.7.1`) and hopefully the build will succeed
eventually.

You need to publish the following images (click on the ">" sign to the left from the image to expand the section,
 select "mark with latest tag" checkbox):
* https://connect.redhat.com/project/850021/images (Operator)
* https://connect.redhat.com/project/5718431/images (Init Database)
* https://connect.redhat.com/project/4276491/images (Init Ops Manager)
* https://connect.redhat.com/project/4276451/images (Init AppDB)

The following images won't be published by release process, shown here just for reference:
* https://connect.redhat.com/project/851701/images (Database)
* https://connect.redhat.com/project/2207181/images (Ops Manager)
* https://connect.redhat.com/project/2207271/images (AppDB)

## 8. Operator Daily Builds

The outcome of the execution of the `release_quay`
task *will not be new Images published but instead*:

1. A Dockerfile corresponding to this version & distro will be uploaded to S3 ([example Dockerfile for 1.9.0/ubuntu](https://enterprise-operator-dockerfiles.s3.amazonaws.com/dockerfiles/mongodb-enterprise-operator/Dockerfile.ubuntu-1.9.0) & [example Dockerfile for 1.9.0/ubi](https://enterprise-operator-dockerfiles.s3.amazonaws.com/dockerfiles/mongodb-enterprise-operator/Dockerfile.ubi-1.9.0)).
2. A context container image, containing all the container context to build this image from scratch ([example Container image](https://quay.io/mongodb/mongodb-enterprise-operator:1.9.0-context))

These 2 artifacts will be used daily to produce new builds of the image in
question. The task that's responsible for this is the Evergreen alias:
`periodic-builds` which *will be executed daily*. This periodic build is
executed everyday at midnight, thus, the first published image of this version
of the operator will be available at that time and not before.

The results of the periodic builds will appear as notifications in the
[#k8s-operator-daily-builds](https://mongodb.slack.com/archives/C01HYH2KUJ1)
Slack channel.

## Publish public repo

First make sure that the `/public` directory is up to date with the public
repository. This may involve creating a new PR into the development repository
with any changes that have yet to be copied over.

Then run

    scripts/evergreen/update_public_repo.sh <path_to_public_repo_root>

This will copy the contents of the `public` directory in the
`10gen/ops-manager-kubernetes` into the root of the
`mongodb/mongodb-enterprise-kubernetes`, the public repo and will commit changes
(not push!)

This script will also generate YAML files that can be used to install
the operator in clusters with no Helm installed. These yaml files will
be copied into the public repo, they will not exist in the private
repo, and they should not be checked into the private repo either.

Check the last commit in the public repo and if everything is ok - **push it**.

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
