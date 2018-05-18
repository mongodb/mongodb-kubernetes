# Setting Pods Resource Requirements #

It is possible to define specific resource requirements (CPU, Memory
and Storage) for Pods in Kubernetes, this document explains how to do
it with MongoDB resources created with the Ops Manager Operator.

## Resource Definition ##

Resources need to be added in the `yaml` file for a particular
resource, under the Resource `spec` section, a new `resources` section
should be added:

``` yaml
resources:
  cpu: '0.1'
  memory: 512M
  storage: 12G
  storage_class: standard
```

You can find more information about the kind of values that can be
provided for `cpu`, `memory` and `storage` in [this document](https://kubernetes.io/docs/concepts/configuration/manage-compute-resources-container/).

### CPU Resource Requirements ###

*Default Value:* Depends on Kubernetes.

CPUs are measured in CPU *units*. Each *unit* is a virtual CPU (let's
say 1 core). A CPU can be a fraction of a full virtual CPU. For
instance, it can be 0.6 which means 0.6 parts of a 1 full vCPU. 0.6
vCPU is also equivalent to `600m` or *six hundred millicpu*

### Memory Resource Requirements ###

*Default Value:* Depends on Kubernetes.

Memory is measured in bytes, it can be provided as a plain integer, or
using one of the following suffixes: E, P, T, G, M, K. Some examples
are:

* 512M: Half a Gigabyte
* 1G: 1 Gigabyte
* 1Gi: 1 Gibibyte (power of 2 equivalent to G)

### Storage Resource Requirements ###

*Default Value:* 16G.

Storage refers to `PersistentVolume` storage allocated for this Pod, and
it can be anything above 10G for MongoDB to work on. This is the
amount of storage allocated for the `/data` directory, inside the
container,  where MongoDB will store the database files. This storage
will be allocated dynamically by using the configured `storage_class`.

#### Storage Class ####

Defined by your Kubernetes administrator. On a simple installation
(like Minikube) there will be a `standard` storage class that you can
use, but in production installation, or cloud-based Kubernetes
installations, there will be options (different classes) with
different purposes. We can assume there will be a "fast" and a "slow"
class, but also an "encrypted", "local", "remote", "distributed" or
"secure" classes.

This is to allow dynamic provisioning of `PersistentVolume`. Enabling
`StorageClass`es in your cluster will allow the Ops Manager Operator
to allocate the storage it needs for the MongoDB Pods without having
to manually allocate this space beforehand.

Find more information about storage classes
[here](https://kubernetes.io/docs/concepts/storage/storage-classes/).
