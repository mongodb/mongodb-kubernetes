# Summary

Every image of the Kubernetes Operator is rebuilt daily, this means that we will
produce a new image with the newest version of the container’s base image and
supporting libraries each day. A consequence of this is that every time a
supporting library is updated or the container’s base image is updated, we will
produce a new version of our container images with these changes.

# Architecture

When releasing each version of each type of image (operator, database,
init-database, appdb, init-appdb, init-ops-manager and ops-manager) we will save
the Dockerfiles that we can use to rebuild the image each time. For instance,
when releasing the container image for Database with version 2.0.0 a Dockerfile
for this version, and for each supported base image (UBI8 and Ubuntu-1604) is
saved to S3. A daily task will use this saved Dockerfile to produce new versions
of this image.

## Architectural considerations

During daily builds, only the base image and supporting libraries will be
updated. If the image includes a license, script or compiled binary of any type,
these files will be exactly the same that the canonical version holds. For
instance Operator 1.9.1 (canonical image for 1.9.1) and every build for this
version will have an identical Operator binary and no changes will be made to
that file until its version is increased to 1.9.2.

For a more in-depth explanation of the architecture of the solution, read the
Architecture In-Depth section.

# Daily Builds

Daily builds have the same format as the canonical version tag, suffixed with -b
and a `date string` with format `%Y%m%dT%H%M%SZ`. For instance, for the 5th of
February 2021, the operator image was built with the following tags:

* 1.9.0
  * Ubuntu 1604: `quay.io/mongodb/mongodb-enterprise-operator:1.9.0-b20210205T000000Z`
  * UBI8: `quay.io/mongodb/mongodb-enterprise-operator-ubi:1.9.0-b20210205T000000Z` 

* 1.9.1
  * Ubuntu 1604: `quay.io/mongodb/mongodb-enterprise-operator:1.9.1-b20210205T000000Z`
  * UBI8: `quay.io/mongodb/mongodb-enterprise-operator-ubi:1.9.1-b20210205T000000Z`

Versions 1.9.0 and 1.9.1 have been released supporting periodic builds. This
particular build can be found here.

Besides building new versions and tagging the new build tags, the canonical tag
for this version will be rolled-forward and "point" at the same image that the
daily build. This means that the canonical tag for a version will always point
at the most recent build of it.

The daily builds use a specific Evergreen feature called “Periodic Builds”, the
result of these builds won't show up on the project's waterfall, but instead,
Evergreen will report each day's status in the `#k8s-operator-daily-builds`
channel.

# Install a specific build

Using helm you can install any build for a particular day, you just need to find
out what's the build id you want to use. In our previous example, the build is
`b20210205T000000Z`. The Operator repository holds more information about how to
install and run tests on a given build in docs/testing-daily-builds.md.

# Architecture In-Depth

There are 3 main components needed to perform a daily build and these are
configured when any of the images is released.

## S3 Store

At release time, a version for every base image supported (currently UBI8 and
Ubuntu1604) is saved in this S3 location.

## Realm/Atlas Database

Each time a new version is released, it will be added to an Atlas database that
can be read from Realm. The Realm endpoints are:

* Operator images
* Init Database images
* Database images
* Init AppDB images
* AppDB images

And images corresponding to Ops Manager:

* Init Ops Manager images
* Ops Manager images

## Container context image

When releasing any of the images, we will produce both a Dockerfile, and a
context Container image. This container image acts as a "Context" or "PATH" that
will be used when building new images daily.

As an example, when building the daily rebuild of the Operator version 1.9.1, we
need 2 things:
* The context for Operator version 1.9.1
(`quay.io/mongodb/mongodb-enterprise-operator:1.9.1-context`)
* The Dockerfile for this particular.

The 1.9.1-context image includes all the content that is needed for this image
to be built, in the Operator example, this is:

* License files
* Operator compiled binary
* Version manifest

This means that the new rebuild of the image can be executed from anywhere, at
any point in time, without requiring the original source code of the image, or
complex compilation patterns.

## Building a new image daily

There's a periodic build task that will be executed everyday at 11am UTC time,
which produces new builds for each image and version already released. The
process is:

* Fetches a list of released versions for each image supported
* For each version
  * Downloads the corresponding Dockerfile from S3
  * Build the Container image from the Dockerfile and using the context image
* Each new version is tagged with a build suffix (-bXXYYZZ) and the canonical
  version is rolled forward (every day, tag 1.9.1, for instance, will point at
  the latest build)

When the images are rebuilt, the latest version of the base Container image will
be used and appropriate commands to update the system libraries will be
executed. This will result as a new image with the latest security fixes
available.

## Running an openshift-preflight scan on the images

When the daily builds are successful, images are pushed to Quay. To prepare for publishing
those images, a `preflight` check is performed after the daily builds using [openshift-preflight](https://github.com/redhat-openshift-ecosystem/openshift-preflight). 

# FAQ

* What if an image rebuild fails?

The output of the daily tasks is `#k8s-operator-daily-builds`. If anything fails,
then it will be reported as a "failure" for that task. You can click on the
Evergreen link to find what's the reason for the failure.

* What happens when a build fails?

The `@kubernetes-oncall` person will be alerted with a Slack message. The
Evergreen build, linked in the message, contains all the information needed to
investigate and debug the problem.

* How can I access the Atlas and Realm resources?

  * [10gen Cloud Organization](https://cloud.mongodb.com/v2#/org/599eecf19f78f769464d17c0/projects)
  * [Kubernetes Team Cloud Project](https://cloud.mongodb.com/v2/5fb40386308f075a75b2e448#clusters)
  * [Atlas Cluster](https://cloud.mongodb.com/v2/5fb40386308f075a75b2e448#clusters/detail/Cluster0)
  * [Realm App](https://realm.mongodb.com/groups/5fb40386308f075a75b2e448/apps/5ff58e458d99a3b27a2ef44f/dashboard)
