# Automated Release Process

Most of the release process is done by PCT. There is still some human
intervention required, but this will go away as we feel more and more confident
with the automation.

The release process is a collection of images being built and published to
multiple container registries (Quay.io and Redhat registry); and additional
tasks, like updating our public repository, publishing release notes, etc.

The whole process is commanded with the Slack `pct` tool. In the next
paragraphs I explain what you should do to move the release to completion.

During the release process, `pct` will be posting notification messages in the
[#k8s-operator-devs-notifications](https://mongodb.slack.com/archives/C023BA9UKC7)
Slack channel. Each time an _action_ has been executed, `pct` will post a new
message in the release thread.

## Creating a Release Ticket

* Example: [CLOUDP-128343](https://jira.mongodb.org/browse/CLOUDP-128343)

A release ticket needs to be created manually. The following attributes must be set!

* Title: `Release kube-enterprise <version>` _This can be anything_
* State: `Open`
* Due date: Set the expected release data
* Fix Version/s: `kube-enterprise-x.y.z` _This needs to be created beforehand_
* Description: Use `key=value` to change parameters for the release.
  - `minKubeVersion`: Minimum Kubernetes version supported
  - `replaces`: Previous version of the operator
  - `signed_off_by`: "Name <email>" that will be used as `signed_off_by` in commits
    on the "certified" and "community" Redhat operator repos.

## Start release

We need to indicate to PCT that the release should start and for that we'll use:

* `/pct k8s start-release <RELEASE-TICKET>`

Where `<RELEASE-TICKET>` is the CLOUDP ticket that was created in the previous
step.

PCT runs every hour (at around [minute
11](https://github.com/10gen/pct/blob/master/src/environments/cronjobs-prod.yml#L4)).
The first stage of PCT will run; You will then find:

* A new PR has been created in the Enterprise Private repo ([Example](https://github.com/10gen/ops-manager-kubernetes/pull/1962))
* Release ticket has been moved to "_IN PROGRESS_"
* Release PR has been linked to the release ticket
* Release state has been updated to "_IN PROGRESS_" (verify with `/pct k8s status <RELEASE-TICKET>`)

Now you should review the PR and change anything that might be needed, look for
an approval from people working on the big features to be released. When the PR
has received the required approvals, merge it.

## Find Commit-SHA in `master`

After merging the release PR, you'll find a new commit-sha in master, write it
down because we'll need it for:

* `/pct k8s set-release-sha <RELEASE-TICKET> <COMMIT-SHA>`

After PCT runs again, it will TAG the Enterprise repository, which will trigger
a new Evergreen run (with additional "_release_" variants). You will find this
run in the Evergreen [Waterfall](https://evergreen.mongodb.com/waterfall/ops-manager-kubernetes).

## Releasing Context Images

Context images contains binaries built at release time in tag build. 

Process of building them is manual: 
* after finding the Evergreen run that was triggered after the previous step, look for the release variant and
unlock *only the relevant release tasks*. This is going to always be the
"_Operator_" image plus some others.

To unlock a task, click *Override Dependencies* in the blocked task's page in EVG (e.g.: [release_database_task](https://evergreen.mongodb.com/task/ops_manager_kubernetes_release_release_database__1.16.4_22_08_01_10_12_02)
).

## Trigger periodic build manually

In order to speed up release don't wait for daily build triggered by cron, but execute periodic build manually:
[running-manual-periodic-builds.md](running-manual-periodic-builds.md)

## Publish to public repo

After successfully executing periodic build, we'll have every image published in quay.
We can proceed with the release by publishing PRs to public repositories. 

* `/pct k8s ok-to-publish <RELEASE-TICKET>`

The next task for `pct` will be to create a release PR in the following public repositories:
* [mongodb/mongodb-enterprise-kubernetes](https://github.com/mongodb/mongodb-enterprise-kubernetes)
  * ([Example](https://github.com/mongodb/mongodb-enterprise-kubernetes/pull/201)).
* [mongodb/helm-charts](https://github.com/mongodb/helm-charts)

Take a look at this PR, and correct anything that needs correction.
Both PRs have to merged in order to proceed with the release.

Once this PR is merged, a new draft release with the operator version tag will be created for the multicluster CLI binary in https://github.com/mongodb/mongodb-enterprise-kubernetes/releases . Please check and publish this release.

### Generating openshift bundles and digest pinning
Generating openshift bundles and digest pinning is performed automatically **in the first** periodic (daily) after tag build, which publishes -context images.

In case of problems, the following places are to be checked:
* `.evergreen-periodic-builds.yaml`: 
  * variant `prepare_openshift_bundles`
  * task `run_conditionally_prepare_and_upload_openshift_bundles`, which checks if it should generate bundles
    * executes condition script to check if bundle files exists in S3: `scripts/evergeen/should_prepare_openshift_bundles.sh` 
  * if there are no generated bundles, then `prepare_and_upload_openshift_bundles` task is executed 

In order to force generating bundles again, delete both files manually from S3:
* certified_bundle: `https://operator-e2e-bundles.s3.amazonaws.com/bundles/operator-certified-${version}.tgz`
* community_bundle: `https://operator-e2e-bundles.s3.amazonaws.com/bundles/operator-certified-${version}.tgz`

## Verify the Community and Certified bundles

PCT will create two Pull Request that should get merged automatically. Look them up here:
* https://github.com/redhat-openshift-ecosystem/certified-operators/pulls
* https://github.com/k8s-operatorhub/community-operators/tree/main/operators/pulls

The former is used for the OpenShift Marketplace available in OpenShift admin console. The latter is used for OLM
installed on vanilla Kubernetes.

Check if `manifests/mongodb-enterprise.clusterserviceversion.yaml` in certified operator contains `sha256` digests instead of versions. 

## Publish release notes

After the public release PR has been merged, `pct` will create release notes:

- As a _draft_ release in the public repo
- A DOCSP will be created with the same release notes


**Note**: PCT does not yet update Operator/Ops Manager compatibility docs.
You must manually create a DOCSP ticket with the correct Operator/OM versions [here](https://docs.mongodb.com/kubernetes-operator/master/tutorial/plan-k8s-op-compatibility/#cloud-short-and-onprem-versions).

What you have to do now is to check that draft release, and _Publish_ it. Also
alert the DOCS team that the release notes are ready for them.

- _The release notes ticket will be linked to the release ticket_

## Finalize release

The final step on the release is to _finalize_ it, which will make `pct` close
the release ticket and send a release email. In order to do so:

* `/pct k8s finalize-release <RELEASE-TICKET>`

After `pct` execution, make sure that:

- Release ticket is in _RESOLVED_ state
- Release status is in _FINISHED_ state (`/pct k8s status <RELEASE-TICKET>`)
- You have received a release email from _private-cloud-kubernetes@10gen.com_
