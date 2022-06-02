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

* Example: [CLOUDP-97129](https://jira.mongodb.org/browse/CLOUDP-97129)

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

## Releasing Images

This process still needs to be done manually; after finding the Evergreen run
that was triggered after the previous step, look for the release variant and
unlock *only the relevant release tasks*. This is going to always be the
"_Operator_" image plus some others.

In case you need these images to be published earlier, you can trigger a manual
rebuild (causing the new images to be published) following the [docs](./running-manual-periodic-builds.md).

### Note on Published Images

The current process releases new images to [Quay.io](https://quay.io/organization/mongodb)
and Redhat Connect. The later requires the images to be _published_ manually, before
they can be fetched or pulled. _This is not required for Quay_.

To publish the collection of images in RedHat, visit:

* https://connect.redhat.com/project/850021/images (Operator)
* https://connect.redhat.com/project/5718431/images (Init Database)
* https://connect.redhat.com/project/4276491/images (Init Ops Manager)
* https://connect.redhat.com/project/4276451/images (Init AppDB)
* https://connect.redhat.com/project/851701/images (Database)
* https://connect.redhat.com/project/5961821/images (AppDB Database)
* https://connect.redhat.com/project/5961771/images (MongoDB Agent)

And make sure the relevant images are set to published.

## Publish to public repo

Because how our release process works, we'll have to wait until *next day*
before continuing the process (after daily rebuilds have run at ~4am). Check
that the relevant images have been pushed to Quay and then run:

* `/pct k8s ok-to-publish <RELEASE-TICKET>`

The next task for `pct` will be to create a release PR on the public repository
([Example](https://github.com/mongodb/mongodb-enterprise-kubernetes/pull/201)).
Take a look at this PR, and correct anything that needs correction. Merge when
it looks Ok.

## Publish release notes

After the public release PR has been merged, `pct` will create release notes:

- As a _draft_ release in the public repo
- A DOCSP will be created with the same release notes


**Note**: PCT does not yet update Operator/Ops Manager compatibility docs.
You must manually create a DOCSP ticket with the correct Operator/OM versions [here](https://docs.mongodb.com/kubernetes-operator/master/tutorial/plan-k8s-op-compatibility/#cloud-short-and-onprem-versions).

What you have to do now is to check that draft release, and _Publish_ it. Also
alert the DOCS team that the release notes are ready for them.

- _The release notes ticket will be linked to the release ticket_

## Publish to Community And Certified Operators

`PCT` will create 2 PRs over "k8s-operatorhub/community-operators" and
"redhat-openshift-ecosystem/certified-operators", for the new version of the
operator, that integrates with Operator Hub and Openshift Operators.

These PRs will be tested and merged by Redhat so there should not be any more
interaction between us. If the PRs test fail, the "approvers" in
(`operators/mongob-enterprise/ci.yaml`) will be notified.

## Finalize release

The final step on the release is to _finalize_ it, which will make `pct` close
the release ticket and send a release email. In order to do so:

* `/pct k8s finalize-release <RELEASE-TICKET>`

After `pct` execution, make sure that:

- Release ticket is in _RESOLVED_ state
- Release status is in _FINISHED_ state (`/pct k8s status <RELEASE-TICKET>`)
- You have received a release email from _private-cloud-kubernetes@10gen.com_
