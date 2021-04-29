# Quarterly Updates

This document describes the process in which the Kubernetes Operator team will
update modules, 3rd party libraries, tools, programming languages, etc., when
needed. We have pre-allocated 1 engineering week for this to be done; however,
updates can happen at any time!

## Review Dependabot PRs

* **Estimated effort: half day.**

Dependabot will take care of most of the library updates on our codebase, your
task will be to check each one of the PRs on the following list and:

1. Check that updating the library *makes sense*. Dependabot is an automated
   tool and as such, can make mistakes, for instance, it could try to update to
   a non released library version or similar
   ([example](https://github.com/10gen/ops-manager-kubernetes/pull/1492)).
2. Authorize the Evergreen patch (clicking on the Evergreen run).
3. Wait for tests to complete
4. Merge PR!

* **Important Note on k8s.io Golang libraries**. `k8s.io` Go modules are based
  on the Kubernetes version they support. As an example:

  1) `k8s.io/apimachinery 0.20.0` was released with Kubernetes 1.20
  2) `k8s.io/apimachinery 0.21.0` was released with Kubernetes 1.21

The following dependencies are part of Kubernetes:

| Module name           | Version | Kubernetes version |
|-----------------------|---------|--------------------|
| k8s.io/api            | v0.20.2 | 1.20               |
| k8s.io/apimachinery   | v0.20.2 | 1.20               |
| k8s.io/client-go      | v0.20.2 | 1.20               |
| k8s.io/code-generator | v0.20.2 | 1.20               |

### For Enterprise Operator

* [Go modules](https://github.com/10gen/ops-manager-kubernetes/pulls?q=is%3Aopen+is%3Apr+author%3Aapp%2Fdependabot+label%3Ago)
* [Python libraries](https://github.com/10gen/ops-manager-kubernetes/pulls?q=is%3Aopen+is%3Apr+author%3Aapp%2Fdependabot+label%3Apython)

### For Community

* [Go modules](https://github.com/mongodb/mongodb-kubernetes-operator/pulls?q=is%3Apr+is%3Aopen+label%3Ago)
* [Python libraries](https://github.com/mongodb/mongodb-kubernetes-operator/pulls?q=is%3Apr+is%3Aopen+label%3Apython)

## Update to latest Operator-SDK

* **Estimated effort: *MINOR* update -> half day. *MAJOR* update -> 2 days.**

To not fall behind the latest versions of `operator-sdk` make sure you update to
the latest one in the [releases
page](https://github.com/operator-framework/operator-sdk/releases). You will
find more information about Operator-SDK's upgrade process
[here](https://sdk.operatorframework.io/docs/upgrading-sdk-version/).

## Update Programming Languages/Toolchains

The Enterprise and Community Operators use both Golang and Python in multiple
places. Make sure you are using the latest possible all the time!

### Golang

* Update Golang in `.evergreen.yml` (search for `go_options`)
* Update Golang to the same as the previous point in every `Dockerfile` (search
  for `^from golang:`).

**These should not be updated until that particular version of Kubernetes is
supported.** Look at our [platform
support](https://docs.google.com/spreadsheets/d/1x5vfesgCaGJbFI07OPNRgOAxSIZIAxrJcRME8qjuvcw/edit#gid=0)
to find the version of Kubernetes we'll support on our current release for
minor, or next release for major.

### Python

* Update Python version in `.evergreen.yml` file (search for `python_env`) func.
* Same in `.evergreen-periodic-builds.yaml`.
* Update `mongodb-enterprise-tests/Dockerfile`.

## Review Base Images

If any of the base images is going End of Life in the next 12 months, create an
update ticket and check with your team's Lead.

* **Ubuntu**: Make sure our current Ubuntu images is still supported in [Ubuntu
  Release Cycle](https://ubuntu.com/about/release-cycle).
* **UBI**: UBI8 was recently published and it will be supported for about 10
  years. However, it is important to be aware of new versions that could be
  deployed and how our product could benefit from them.

## Tools Updates

We use a multitude of tools when developing the operator, especially during test
runs. Look at the following tools and make sure everything is updated to the
latest versions:

### Enterprise

* [Kind](../scripts/evergreen/setup_kind.sh)
* [Kubectl](../scripts/evergreen/setup_kubectl.sh)
* [Helm](../scripts/evergreen/setup_kubectl.sh)
* [Kops](../scripts/evergreen/setup_kubernetes_environment.sh)
* [Openshift](https://console-openshift-console.apps.openshift.mongokubernetes.com/settings/cluster).
  There is an update process that can be initiated from the UI.
* [Minikube](../scripts/evergreen/setup_minikube.sh). Minikube is always installed
  from `latest`, on every test so there is no need to update it manually.


### Community

In community tools are downloaded from the `.evergreen.yml` file. Search for
`command: go run download`, to find a list of tools downloaded with this method.
Just update the versions and create a PR to see if everything still works.

## Documentation

Make sure all of the docs are up-to-date! Go through them and:

1. Remove the ones that are not relevant any more. If you think this is still
   valuable, for historical reasons, make sure you add a big DEPRECATED note at
   the beginning of the document!
2. Update the ones pointing at old instructions.

## Other Dependencies

There are a few other dependencies that you might want to find to update:

* [Cert-manager Helm Chart](../docker/mongodb-enterprise-tests/tests/conftest.py).
  Latest should be
  [here](https://artifacthub.io/packages/helm/microfunctions/cert-manager).
* [LDAP Helm Chart](../docker/mongodb-enterprise-tests/tests/authentication/conftest.py).
