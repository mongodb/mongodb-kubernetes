# RedHat Community Operators

* **Goal**: Publish our newest version to [operatorhub](https://operatorhub.io).

First, fork the following repo into you own:

    git@github.com:operator-framework/community-operators.git

Make sure you clone the *fork* and not *upstream*.

* More information about working with forks can be found
[here](https://help.github.com/en/articles/fork-a-repo).

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


Now, in the clusterserviceversion file, there are a few attributes you'll have to change:

## Metadata

* `metadata.name`: The name should include the latest version
* `metadata.containerImage`: Should point to the last version (**not latest**) in RedHat Connect catalog.
* `metadata.createdAt`: `$(date +%Y-%m-%dT%H:%M:%SZ)`

## Spec

* `spec.version`: Add your new version with 3 parts (x.y.z)
* `spec.replaces`: Indicate the last version that is replaced by this one

## CustomResourceDefinitions

The CRDs need to be updated if there's anything to be updated. This is basically the
same that we have in the `crds.yaml` file.

## Package

Update the file `mongodb-enterprise.package.yaml` making the
`channels[?@.name==stable].currentCSV` section to point to this release.

## Creating a PR with your changes

When finished, commit these changes into a new branch, push to your
fork and create a PR against the parent repo, the one from where you forked yours).

Look at this [example](https://github.com/operator-framework/community-operators/pull/540)

There are a few tests that will run on your PR and if everything goes
fine, one of the maintainers of `community-operators` will merge your
changes. The changes will land on `operatorhub.io` after a few days.

# Openshift Marketplace

* **Goal**: Publish our newest version to [Openshift Marketplace](https://www.openshift.com/).

This file is very similar to the one in
`community-operators/upstream-community-operators/mongodb-enterprise`
with the difference that the docker images in these manifest files
point at the RedHat Connect catalog and not to Quay.io.

The process of building a new version is to just take the latest CSV,
created in the previous step, and to change the Docker registry from
Quay to RedHat Connect.

* Pro tip: Use your favorite search/replace tool to change
  `quay.io/mongodb/mongodb-enterprise-` for
  `registry.connect.redhat.com/mongodb/enterprise-`.

## Compress and Upload

After the new file has been updated, it needs to be compressed as a zip
file:

    zip -r mongodb-enterprise.v<x>.<y>.<z>.zip .

Finally, the zip file needs to be uploaded to the [Operator
Metadata](https://connect.redhat.com/project/850021/operator-metadata)
section in RedHat connect.

The process of verification takes around 1 hour.
