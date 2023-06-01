# Release <x.y.z version>

*For in-depth information of how to perform any of these steps, please read
refer to [how-to-release](../../docs/dev/release/how-to-release.md) document.*

# Pre-merging tasks

*We'll make sure all the following tasks are completed before merging.*

## Project Tasks

- [ ] Update any finished ticket's `Fix Version` to this version.
- [ ] Prepare release notes in [public repo](https://github.com/mongodb/mongodb-enterprise-kubernetes/releases/new).
- [ ] Prepare release notes in [DOCSP](https://jira.mongodb.org/secure/CreateIssueDetails!init.jspa?pid=14181&issuetype=3&summary=[MEKO]%20Kubernetes%20Enterprise%20Operator%20x.y.z%20Release%20Notes).

## Versioning

- [ ] Ensure that all of versions in release.json are correct and are properly reflected in `values.yaml` and `values-openshift.yaml`.

## Public repo

- [ ] All public samples are up-to-date.

# Post-merging tasks

*After merging this PR make sure you complete the following tasks.*

- [ ] Unblock relevant release tasks from Evergreen's Waterfall.
- [ ] Update public repo contents.

## Openshift/RedHat Changes

*Refer to
[publishing-to-marketplaces.md](docs/dev/release/publishing-to-marketplaces.md)
for more infomation on how to do this*

- [ ] New version has been pushed to Operatorhub.io.
- [ ] New version has been pushed to Openshift Marketplace.

# Following day

- [ ] Check [#k8s-operator-daily-builds](https://mongodb.slack.com/archives/C01HYH2KUJ1) to see if this version was pushed correctly.
- [ ] Publish public repo tag/release.
- [ ] Ask DOCS team to publish Release Notes.
