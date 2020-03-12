# RedHat Community Operators

* **Goal**: Publish our newest version to [operatorhub](https://operatorhub.io).

## Fork/pull changes from community-operators
### If you do this the first time
Fork the following repo into you own:

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

In that last example, we see 3 versions published: `0.3.2`, `0.9.0`,
`1.1.0` and others. Let's assume you are working on publishing version
`1.3.0`. You'll start by copying the last version directory:

    ```bash
    cp -r 1.2.4 1.3.0
    ```

Then renaming the clusterserviceversion file:
    ```bash
    cd 1.3.0
    mv mongodb-enterprise.v1.2.4.clusterserviceversion.yaml mongodb-enterprise.v1.3.0.clusterserviceversion.yaml
    ```

## Update clusterserviceversion file

### Metadata

* `metadata.name`: The name should include the latest version
* `metadata.containerImage`: Should point to the last version (**not 'latest'**) in quay.io repository.
* `metadata.createdAt`: `$(date +%Y-%m-%dT%H:%M:%SZ)`

### Spec

* `spec.version`: Add your new version with 3 parts (x.y.z)
* `spec.replaces`: Indicate the last version that is replaced by this one

### Operator

* `install.spec.deployments` - update the versions of Database and Operator images
* Check if anything has changed for the Operator deployment spec that needs to be 
reflected in `install.spec.deployments` (environment variables etc)

### Permissions

Copy the `rules` from roles in `roles.yaml` to the `permissions.rules` element to make sure the permissions are up-to-date

## CustomResourceDefinitions

The CRD files need to be updated if there's anything to be updated. This is basically the
same that we have in the `crds.yaml` file.

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

# Openshift Marketplace

* **Goal**: Publish our newest version to [Openshift Marketplace](https://www.openshift.com/).

This file is very similar to the one in
`community-operators/upstream-community-operators/mongodb-enterprise`
with the difference that the docker images in these manifest files
point at the RedHat Connect catalog and not to Quay.io.

Copy the file to some temporary location and edit it:

```
cp mongodb-enterprise.vX.Y.Z.clusterserviceversion.yaml /tmp/mongodb-enterprise.vX.Y.Z.clusterserviceversion.yaml
```

Change the Docker registry from Quay to RedHat Connect:

* Change  `quay.io/mongodb/mongodb-enterprise-` to
  `registry.connect.redhat.com/mongodb/enterprise-`. for the Operator and Database
* Change  `quay.io/mongodb` to `registry.connect.redhat.com/mongodb` for `OPS_MANAGER_IMAGE_REPOSITORY`
and `APP_DB_IMAGE_REPOSITORY`
  

## Compress and Upload

After the new file has been updated, it needs to be compressed as a zip
file:

    zip -r mongodb-enterprise.vX.Y.Z.zip mongodb-enterprise.vX.Y.Z.clusterserviceversion.yaml

Finally, the zip file needs to be uploaded to the [Operator
Metadata](https://connect.redhat.com/project/850021/operator-metadata)
section in RedHat connect.

The process of verification takes around 1 hour.
