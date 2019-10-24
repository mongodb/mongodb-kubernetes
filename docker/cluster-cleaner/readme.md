# Cluster-Cleaner

The `cluster-cleaner` is a series of scripts in a Docker image that it is
supposed to run on the Kubernetes cluster via CronJobs.

## Installing a new version

When making changes to any of the scripts, or adding CronJobs, the image needs
to be rebuild, just increase the version number both in `Chart.yaml` and
`Makefile`. The process of publishing a new version is:

* Make changes to bash scripts and CronJob yaml files
* Run `make build && make push`

The CronJobs will be installed, and they will always point at the `latest`
version.
