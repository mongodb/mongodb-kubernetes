# Openshift Marketplace

## Abstract

RedHat supports many different distribution channels and it's sometimes hard to distinguish them. The mechanisms of working
with them change often and it important to understand base principle.

The following are the main places of distribution:

### (public) OperatorHub (https://operatorhub.io/).
The mechanism of adding new Operator versions there is by forking the
GitHub repo and creating a PR. This relates to the `upstream-community-operators` directory in the GitHub repo (described [below](#community))
Some other redhat [documentation](https://redhat-connect.gitbook.io/certified-operator-guide/troubleshooting-and-resources/submitting-a-community-operator-to-operatorhub.io#submission-process-overview) describing the process

### (embedded) OperatorHub.

What the docs [says](https://redhat-connect.gitbook.io/partner-guide-for-red-hat-openshift-and-container/certify-your-operator/certify-your-operator-bundle-image):

> Certified operators are listed in and consumed by customers through the embedded OpenShift OperatorHub, providing them
> the ability to easily deploy and run your solution. Additionally, your product and operator image will be listed in
> the Red Hat Container Catalog using the listing information you provide.‌

It seems that this OperatorHub is the one embedded into the Openshift cluster and can be accessed either using UI or CLI.

There are two different (and contradicting) documentation articles from RedHat describing the way to update the embedded OperatorHub:
1. The PR for a forked repository: https://redhat-connect.gitbook.io/certified-operator-guide/troubleshooting-and-resources/submitting-a-community-operator-to-openshift-operatorhub
(This seems to relate to `community-operators` folder in the GutHub Repo (unlike the public OperatorHub mentioned above))
2. The process of pushing special Operator bundles containing CRD, CSV etc:

[How to build the bundle docker image](https://redhat-connect.gitbook.io/certified-operator-guide/ocp-deployment/operator-metadata/creating-the-metadata-bundle)

[How to upload the bundle image](https://redhat-connect.gitbook.io/partner-guide-for-red-hat-openshift-and-container/certify-your-operator/certify-your-operator-bundle-image/uploading-your-operator-bundle-image)


### RedHat Marketplace (https://marketplace.redhat.com/en-us).
This is a platform allowing to install certified Operators into
the Openshift cluster using a special Marketplace Operator. It seems to be similar to OperatorHub from the functional point of view
but it provides billing and licensing support. The way for us to publish is described
in the [next section](#marketplace)


These are the other terms that may be met in different places:
* [Red Hat Container Catalog](https://catalog.redhat.com/): This seems to be a catalog providing the short information
about different Operators - the specific deployment instructions lead to the "embedded OperatorHub".

## <a name="marketplace"></a> Publishing to [Openshift Marketplace](https://marketplace.redhat.com/en-us).

``` bash

release_before_last="1.8.1"
prev_release="1.8.2"
current_release="1.9.0"

cd bundle
sed -i '' "s/${prev_release}/${current_release}/g" mongodb-enterprise.package.yaml
mkdir ${current_release}
ls ${prev_release}/*.yaml | xargs -n1 -I{file} cp {file} ${current_release}
cd ${current_release}
mv mongodb-enterprise.v${prev_release}.clusterserviceversion.yaml mongodb-enterprise.v${current_release}.clusterserviceversion.yaml
# update all references to the new version
sed -i '' "s/${prev_release}/${current_release}/g" mongodb-enterprise.v${current_release}.clusterserviceversion.yaml
# update reference to the previous version
sed -i '' "s/${release_before_last}/${prev_release}/g" mongodb-enterprise.v${current_release}.clusterserviceversion.yaml
# update the CRDs
cp ../../public/helm_chart/crds/mongodb.mongodb.com.yaml mongodb.mongodb.com.crd.yaml
cp ../../public/helm_chart/crds/mongodbusers.mongodb.com.yaml mongodbusers.mongodb.com.crd.yaml
cp ../../public/helm_chart/crds/opsmanagers.mongodb.com.yaml opsmanagers.mongodb.com.crd.yaml
cd ..
```

### Check for Changes

Check for any [new changes](./csv-changes.md) that have been introduced since the last release.

### Metadata

* `metadata.createdAt`: `$(date +%Y-%m-%dT%H:%M:%SZ)`

### Operator

* Check if anything has changed for the Operator deployment spec that needs to be
reflected in `install.spec.deployments` (environment variables, init images versions etc)

### Permissions

Copy the `rules` from roles in `roles.yaml` to the `permissions.rules` element to make sure the permissions are up-to-date

## Upload
The new way of uploading our operator to RedHat is by using the new `bundle` format.
A RedHat-provided guide can be found [here](https://redhat-connect.gitbook.io/certified-operator-guide/appendix/bundle-maintenance-after-migration), but this guide will
cover the steps needed:

### Prerequisites
You need to have [opm](https://docs.openshift.com/container-platform/4.5/operators/admin/olm-managing-custom-catalogs.html#olm-installing-opm_olm-managing-custom-catalogs).
The guide linked here uses podman, but the same can be easily achieved with `docker`:

1. Move to an empty directory:
```bash
    mdkir tmp
    cd tmp
```
2. ` docker login https://registry.redhat.io -u mongodb-inc` (You should have the password for this account. If you don't, ask your teammates)
3. Obtain `opm`
   - If you have [oc](https://docs.openshift.com/container-platform/4.6/cli_reference/openshift_cli/getting-started-cli.html) installed, then you can run this command:
   ```bash
   oc image extract registry.redhat.io/openshift4/ose-operator-registry:v4.6 --filter-by-os amd64 \
    --path /usr/bin/darwin-amd64-opm:.
   ```
   Note: if you don't have a `macOs amd64` machine, you will need to use different arguments.

   Note: make sure to be in an *empty* directory when running this command, otherwise it will fail (or, if ran with `--confirm`, it will overwrite the directory)
   - If you don't have `oc` installed, you should be able to obtain the same result with:
   ```
   container_id=`docker create registry.redhat.io/openshift4/ose-operator-registry:v4.6`
   docker cp $container_id:/usr/bin/darwin-amd64-opm .
   docker rm $container_id
   ```
4. (Optional) move the `opm` binary somewhere in your `PATH`

## Steps
1. Let `opm` rearrange files and generate the Dockerfile:
```bash
cd bundle
opm alpha bundle generate -d ./${current_release} -u ./${current_release}
```
This will generate a `Dockerfile` called `bundle.Dockerfile`.

2. Add the following `LABEL`s to the Dockerfile:
```bash
LABEL com.redhat.openshift.versions="v4.5,v4.6"
LABEL com.redhat.delivery.backport=true
LABEL com.redhat.delivery.operator.bundle=true
```

3. Build this image and tag it as explained [here](https://connect.redhat.com/project/5894371/images/upload-image)
Note: the guide uses podman but the exact same results can be obtained with `docker`

4. After the image has been pushed, you should be able to see it [here](https://connect.redhat.com/project/5894371/images).
NOTE: It usually takes some minutes for it to appear.

5. When the image passes the `Certification Test`, you can finally publish it!

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
cp -r ${GOPATH}/src/github.com/10gen/ops-manager-kubernetes/bundle/csv/X.Y.Z .
```


## Update clusterserviceversion file

Change the Docker registry from Quay to RedHat Connect:

* Change `registry.connect.redhat.com/mongodb/enterprise-` to `quay.io/mongodb/mongodb-enterprise-`
  for the Operator and Database
* Change `registry.connect.redhat.com/mongodb` to `quay.io/mongodb` for `OPS_MANAGER_IMAGE_REPOSITORY`,
`APP_DB_IMAGE_REPOSITORY`, `INIT_OPS_MANAGER_IMAGE_REPOSITORY`, `INIT_APPDB_IMAGE_REPOSITORY` and `INIT_APPDB_VERSION`

## Package

Go back to `mongodb-enterprise` directory.
Update the file `mongodb-enterprise.package.yaml` making the
`channels[?@.name==stable].currentCSV` section to point to this release.

## Creating a PR with your changes

When finished, commit these changes into a new branch, push to your
fork and create a PR against the parent repo, the one from where you forked yours).

Look at this [example](https://github.com/operator-framework/community-operators/pull/540)

There are a few tests that will run on your PR and if everything goes
fine, one of the maintainers of `community-operators` will merge your
changes. The changes will land on `operatorhub.io` after a few days.

The main things to consider before requesting review:
* https://operatorhub.io/preview - make sure the CSV file is parsed correctly and the preview for the Operator
looks correct
* the other checks in the PR are green (the commits must be squashed and signed - check the failed jobs for the
hints how to do this)
