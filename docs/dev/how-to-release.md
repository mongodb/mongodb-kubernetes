# Operator Release

The release procedure means building all necessary images and pushing them to image repositories. 
Also all relevant places are upgraded (Openshift Marketplace, public Github repository) and 
release notes are published.

The images released are:
- operator
- database
- ops manager
- appdb  

The Operator and the Database image follow a simple versioning schema (1.2.0, 1.2.1...).
The Ops Manager and AppDB images use a composite versioning schema <OM_version>-operator<Operator_version> 
Each release publishes a new set of all supported Ops Manager + AppDB images.  

The release process is documented below:

## Update Jira to reflect the patches in the release (Tuesday)

Update any finished tickets in [kube-enterprise-next](https://jira.mongodb.org/issues/?jql=project%20%3D%20CLOUDP%20AND%20component%20%3D%20Kubernetes%20AND%20status%20in%20(Resolved%2C%20Closed)%20and%20fixVersion%3D%20kube-enterprise-next%20%20ORDER%20BY%20resolved%20) to have the version of the release you're doing (kube-x.y)

## Prepare Release Notes (Tuesday)

Draft the release notes describing the tickets in the current
[fixVersion](https://jira.mongodb.org/issues/?jql=project%20%3D%20CLOUDP%20AND%20component%20%3D%20Kubernetes%20AND%20status%20in%20(Resolved%2C%20Closed)%20and%20fixVersion%3D%20kube-enterprise-next%20).

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
./scripts/evergreen/update_release_version.py --operator_version <operator_version> --init_opsmanager_version <init_om_version> --init_appdb_version <init_appdb_version>
```
This will update all relevant files with a new version (you can specify only the containers that have changed)
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

The following images are expected to get released by the end of this procedure:
* Operator
* Database 
* Ops Manager (all supported versions)
* AppDB (all supported versions)

To perform release it's necessary to manually override dependencies in the tasks in the following 
Evergreen build variants (after the release branch was merged):
* release_quay (will deploy all images to quay.io)
* release_rh_connect (will deploy all images to Red Hat Connect)

**Caution (quay.io)**: quay.io doesn't allow to block tags from overwriting, the
current tagged images will be overwritten by `evergreen` if they have
the same tag as any old images.

**Caution (Red Hat Connect)**: so far the build service is not reliable and some checks may fail - you need to trigger new builds on the
site manually specifying the new version (increase the patch part, e.g. `0.7.1`) and hopefully the build will succeed
eventually.

Finally publish the images manually:
* https://connect.redhat.com/project/850021/view (Operator)
* https://connect.redhat.com/project/851701/view (Database)
* https://connect.redhat.com/project/2207181/view (Ops Manager)
* https://connect.redhat.com/project/2207271/view (Ops Manager AppDB)

(note, that the last published image gets the tag "latest" so you should make sure that you publish Ops Manager
 and AppDB images in the ascending order of versions (e.g. `4.2.3` before `4.2.4`))


## Publish public repo

First make sure that the `/public` directory is up to date with the public
repository. This may involve creating a new PR into the development repository
with any changes that have yet to be copied over.

Then run

    scripts/evergreen/update_public_repo.sh <path_to_public_repo_root>

This will copy the contents of the `public` directory in the `10gen/ops-manager-kubernetes` into
the root of the `mongodb/mongodb-enterprise-kubernetes`, the public repo and will commit changes (not push!)

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
