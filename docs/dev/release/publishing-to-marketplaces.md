# Openshift Marketplace

## Abstract

RedHat supports many different distribution channels and it's sometimes hard to
distinguish them. The mechanisms of working with them change often and it
important to understand base principle.

The following are the main places of distribution:

### (public) OperatorHub (https://operatorhub.io/).
The mechanism of adding new Operator versions there is by forking the GitHub
repo and creating a PR. This relates to the `upstream-community-operators`
directory in the GitHub repo (described [below](#community)). Some other redhat
[documentation](https://redhat-connect.gitbook.io/certified-operator-guide/troubleshooting-and-resources/submitting-a-community-operator-to-operatorhub.io#submission-process-overview)
describing the process.

### (embedded) OperatorHub.

What the docs
[says](https://redhat-connect.gitbook.io/partner-guide-for-red-hat-openshift-and-container/certify-your-operator/certify-your-operator-bundle-image):

> Certified operators are listed in and consumed by customers through the
> embedded OpenShift OperatorHub, providing them the ability to easily deploy
> and run your solution. Additionally, your product and operator image will be
> listed in the Red Hat Container Catalog using the listing information you
> provide.‌

It seems that this OperatorHub is the one embedded into the Openshift cluster
and can be accessed either using UI or CLI.

There are two different (and contradicting) documentation articles from RedHat
describing the way to update the embedded OperatorHub:

1. The PR for a forked repository:
https://redhat-connect.gitbook.io/certified-operator-guide/troubleshooting-and-resources/submitting-a-community-operator-to-openshift-operatorhub
(This seems to relate to `community-operators` folder in the GutHub Repo (unlike
the public OperatorHub mentioned above))

2. The process of pushing special Operator bundles containing CRD, CSV etc:

[How to build the bundle docker image](https://redhat-connect.gitbook.io/certified-operator-guide/ocp-deployment/operator-metadata/creating-the-metadata-bundle)

[How to upload the bundle image](https://redhat-connect.gitbook.io/partner-guide-for-red-hat-openshift-and-container/certify-your-operator/certify-your-operator-bundle-image/uploading-your-operator-bundle-image)


### RedHat Marketplace (https://marketplace.redhat.com/en-us).

This is a platform allowing to install certified Operators into the Openshift
cluster using a special Marketplace Operator. It seems to be similar to
OperatorHub from the functional point of view but it provides billing and
licensing support. The way for us to publish is described in the [next
section](#marketplace).

These are the other terms that may be met in different places:

* [Red Hat Container Catalog](https://catalog.redhat.com/): This seems to be a
  catalog providing the short information about different Operators - the
  specific deployment instructions lead to the "embedded OperatorHub".

## <a name="marketplace"></a> Publishing to [Openshift Marketplace](https://marketplace.redhat.com/en-us).

Do this after you have completed the release, and the
`registry.connect.redhat.com/mongodb/enterprise-operator` registry has been
updated with the latest operator version.

* The value of the `rhc_operator_bundle_pid` variable can be found
  in the [Evergreen
  project](https://evergreen.mongodb.com/projects##ops-manager-kubernetes).
  
* The file
  `config/manifests/bases/mongodb-enterprise.clusterserviceversion.yaml` needs
  to be updated, to reflect the correct `replaces` and `minKubeVersion`
  attributes.

```bash
echo "${rhc_operator_bundle_pid}" | docker scan.connect.redhat.com login -u unused --password-stdin

VERSION="$(jq .mongodbOperator -r release.json)"
BUNDLE_IMG="scan.connect.redhat.com/ospid-52d1c6df-b3f6-432b-9646-adb7f689e581/operator-bundle:${VERSION}"

make bundle-annotated "VERSION=${VERSION}" IMG="registry.connect.redhat.com/mongodb/enterprise-operator:${VERSION}"
```

After this process, you will have a new directory corresponding to the version
to release under `./bundle`. Before continuing pushing the bundle to RedHat, we
have to *downgrade* our CRD version to `v1beta1`. *This is a temporary measure
and should be resolved before Kubernetes 1.22 is released around July 2021.*

* **If the CRDs have not been modified since previous release**, just copy all the
  CRDs exactly as they were last time:

``` shell
cp bundle/<previous-version>/manifests/mongodb.com_* bundle/<this-version>/manifests
```

* **If the CRDs have been modified since last time**, *downgrade them to CRD
  v1beta1*. Follow [this
  document](https://kubernetes.io/docs/reference/using-api/deprecation-guide/#customresourcedefinition-v122)
  to know how to do this.

After your CRDs are in the right version, execute the following commands:

``` shell
make bundle-build EXPIRES= "VERSION=${VERSION}" "BUNDLE_IMG=${BUNDLE_IMG}"
make docker-push "VERSION=${VERSION}" "IMG=${BUNDLE_IMG}"
```

After the verification process [have
passed](https://connect.redhat.com/project/5894371/images), create a PR and
commit the changes to the repo. The tests take around 30 minutes to complete.


# <a name="community"></a> RedHat Community Operators

* **Goal**: Publish our newest version to [operatorhub](https://operatorhub.io).

This file is very similar to the one previously generated
with the difference that the docker images in these manifest files
point to Quay.io and not to the RedHat Connect catalog.


## Fork/pull changes from community-operators
### If you do this the first time
Fork the following repo into your own:

    https://github.com/operator-framework/community-operators/tree/master/upstream-community-operators

Make sure you clone the *fork* and not *upstream*.

Add the upstream repository as a remote one

```bash
git remote add upstream git@github.com:operator-framework/community-operators.git
```

* More information about working with forks can be found
[here](https://help.github.com/en/articles/fork-a-repo).

### If you already have the forked repository
Pull changes from the upstream:

```bash
git fetch upstream
git checkout master
git merge upstream/master
```

## Create the new folder for the new version of MongoDB Operator

Go into the
`community-operators/upstream-community-operators/mongodb-enterprise`
directory. Take a look at the list of files in this repo:

``` bash
$ git clone git@github.com:<your-user>/community-operators.git
$ cd community-operators/upstream-community-operators/mongodb-enterprise
$ ls -l
0.3.2
0.9.0
1.1.0
1.2.1
1.2.2
1.2.3
1.2.4
mongodb-enterprise.package.yaml
```

Copy the directory generated in the enterprise repo:

``` bash
cp -r ${GOPATH}/src/github.com/10gen/ops-manager-kubernetes/bundle/X.Y.Z .
```


## Update clusterserviceversion file

Change the Docker registry from Quay to RedHat Connect:

* Change `registry.connect.redhat.com/mongodb/enterprise-` to
  `quay.io/mongodb/mongodb-enterprise-` for the Operator and Database
* Change `registry.connect.redhat.com/mongodb` to `quay.io/mongodb` for
`OPS_MANAGER_IMAGE_REPOSITORY`, `APP_DB_IMAGE_REPOSITORY`,
`INIT_OPS_MANAGER_IMAGE_REPOSITORY`, `INIT_APPDB_IMAGE_REPOSITORY` and
`INIT_APPDB_VERSION`

## Package

Go back to `mongodb-enterprise` directory. Update the file
`mongodb-enterprise.package.yaml` making the
`channels[?@.name==stable].currentCSV` section to point to this release.

## Creating a PR with your changes

When finished, commit these changes into a new branch, push to your
fork and create a PR against the parent repo, the one from where you forked yours).

Look at this [example](https://github.com/operator-framework/community-operators/pull/540)

There are a few tests that will run on your PR and if everything goes
fine, one of the maintainers of `community-operators` will merge your
changes. The changes will land on `operatorhub.io` after a few days.

The main things to consider before requesting review:

* https://operatorhub.io/preview - make sure the CSV file is parsed correctly
  and the preview for the Operator looks correct
* the other checks in the PR are green (the commits must be squashed and
  signed - check the failed jobs for the hints how to do this)
