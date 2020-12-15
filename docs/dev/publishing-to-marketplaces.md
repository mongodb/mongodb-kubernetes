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
in the [next section](#marketplace) (note, that the part describing using zip files is not relevant anymore)


These are the other terms that may be met in different places:
* [Red Hat Container Catalog](https://catalog.redhat.com/): This seems to be a catalog providing the short information 
about different Operators - the specific deployment instructions lead to the "embedded OperatorHub". 

## <a name="marketplace"></a> Publishing to [Openshift Marketplace](https://marketplace.redhat.com/en-us).

**TODO** This path seems to be out-of-date as Operator bundles should be used instead of zip files, this is being confirmed with RedHat now!

We need to produce a zip file with all the versions of the operator: this involves copying the directory and modifying the clusterserviceversion file.

``` bash

release_before_last="1.8.1"
prev_release="1.8.2"
current_release="1.9.0"

cd deploy/csv
sed -i '' "s/${prev_release}/${current_release}/g" mongodb-enterprise.package.yaml
cp -r ${prev_release} ${current_release}
cd ${current_release}
mv mongodb-enterprise.v${prev_release}.clusterserviceversion.yaml mongodb-enterprise.v${current_release}.clusterserviceversion.yaml
# update all references to the new version
sed -i '' "s/${prev_release}/${current_release}/g" mongodb-enterprise.v${current_release}.clusterserviceversion.yaml
# update reference to the previous version
sed -i '' "s/${release_before_last}/${prev_release}/g" mongodb-enterprise.v${current_release}.clusterserviceversion.yaml
# update the CRDs
cp ../../../public/helm_chart/crds/mongodb.mongodb.com.yaml mongodb.mongodb.com.crd.yaml
cp ../../../public/helm_chart/crds/mongodbusers.mongodb.com.yaml mongodbusers.mongodb.com.crd.yaml
cp ../../../public/helm_chart/crds/opsmanagers.mongodb.com.yaml opsmanagers.mongodb.com.crd.yaml
cd ..
```

Check the following sections in the clusterserviceversion file.
You may need to look into https://github.com/mongodb/mongodb-enterprise-kubernetes/blob/master/mongodb-enterprise-openshift.yaml 
as the sourcer of relevant information.

### Metadata

* `metadata.createdAt`: `$(date +%Y-%m-%dT%H:%M:%SZ)`

### Operator

* Check if anything has changed for the Operator deployment spec that needs to be
reflected in `install.spec.deployments` (environment variables, init images versions etc)

### Permissions

Copy the `rules` from roles in `roles.yaml` to the `permissions.rules` element to make sure the permissions are up-to-date

## Compress and Upload

After the new file has been updated, it needs to be compressed as a zip
file alone with everything else in in the mongodb-enterprise directory:

    cd deploy/csv
    zip -r ../redhat_connect_zip_files/mongodb-enterprise.vX.Y.Z.zip .
    git add ../redhat_connect_zip_files/mongodb-enterprise.vX.Y.Z.zip

it should be in the following format
```bash
.
|-- ... all of the previous versions
├── 1.5.2
│   ├── mongodb-enterprise.v1.5.2.clusterserviceversion.yaml
│   ├── mongodb.mongodb.com.crd.yaml
│   ├── mongodbusers.mongodb.com.crd.yaml
│   └── opsmanagers.mongodb.com.crd.yaml
├── 1.5.3
│   ├── mongodb-enterprise.v1.5.3.clusterserviceversion.yaml
│   ├── mongodb.mongodb.com.crd.yaml
│   ├── mongodbusers.mongodb.com.crd.yaml
│   └── opsmanagers.mongodb.com.crd.yaml
└── mongodb-enterprise.package.yaml
```


Finally, the zip file needs to be uploaded to the [Operator
Metadata](https://connect.redhat.com/project/850021/operator-metadata)
section in RedHat connect.

The process of verification can take between 30 minutes and 2 hours. **Note, that you need to refresh the page 
periodically as UI may get stuck in "scanning" that may not be true** 

If after one retry it still fails:
* ask for some help in the "coreos" slack channel (it's a separate organization coreos.slack.com) - you may need to ask for an invitation
* contact RedHat through 
https://connect.redhat.com/support/technology-partner/, the previous support email was connect-tech@redhat.com. In the body please mention **your** email as 
the account is on Jason Mimick and it looks like he will receive the answers
* also there is a possibility to start a more formal support case following these [instructions](https://access.redhat.com/start/how-to-engage-red-hat-support)

After the zip file was successfully scanned and published push it to the current repository so that we can send it to RedHat for Operator certification.

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
cp -r ${GOPATH}/src/github.com/10gen/ops-manager-kubernetes/deploy/csv/X.Y.Z .
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
