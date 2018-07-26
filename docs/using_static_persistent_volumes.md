# Using Static Persistent Volumes #

There are two types of persistent volumes: static and dynamic. The
operator can provision MongoDB object (Standalone, ReplicaSet and
Sharded Cluster) using any of them. This document explains how to use
static persistent volumes.

**This should be considered an example and it is not meant to be used
in production environments**

## Creating a Storage Class ##

We always use the concept of a [Storage
Class](https://kubernetes.io/docs/concepts/storage/storage-classes/)
they will be used when handling both dynamic and persistent volumes.

A simple `StorageClass` can be defined like:

```bash
cat <<EOF | kubectl apply -f -
---
kind: StorageClass
apiVersion: storage.k8s.io/v1
metadata:
  name: basic
  namespace: mongodb
provisioner: kubernetes.io/no-provisioner
EOF

```

Now check that your `StorageClass` was created successfully with:

```
$ oc get sc
NAME      PROVISIONER                    AGE
basic     kubernetes.io/no-provisioner   1m
```

## Creating Persistent Volumes ##

After our `StorageClass` is created, we will create
`PersistentVolumes` that will belong to this class, like in this
example:

```bash
cat <<EOF | kubectl apply -f -
---
kind: PersistentVolume
apiVersion: v1
metadata:
  name: mdb001
  namespace: mongodb
  labels:
    type: local
spec:
  capacity:
    storage: 20Gi
  persistentVolumeReclaimPolicy: Delete
  storageClassName: basic
  accessModes:
    - ReadWriteOnce
  hostPath:
    path: "/tmp/data01"

---
kind: PersistentVolume
apiVersion: v1
metadata:
  name: mdb002
  namespace: mongodb
  labels:
    type: local
spec:
  capacity:
    storage: 20Gi
  persistentVolumeReclaimPolicy: Delete
  storageClassName: basic
  accessModes:
    - ReadWriteOnce
  hostPath:
    path: "/tmp/data02"

---
kind: PersistentVolume
apiVersion: v1
metadata:
  name: mdb003
  namespace: mongodb
  labels:
    type: local
spec:
  capacity:
    storage: 20Gi
  persistentVolumeReclaimPolicy: Delete
  storageClassName: basic
  accessModes:
    - ReadWriteOnce
  hostPath:
    path: "/tmp/data03"

---
kind: PersistentVolume
apiVersion: v1
metadata:
  name: mdb004
  namespace: mongodb
  labels:
    type: local
spec:
  capacity:
    storage: 20Gi
  persistentVolumeReclaimPolicy: Delete
  storageClassName: basic
  accessModes:
    - ReadWriteOnce
  hostPath:
    path: "/tmp/data04"
EOF

```

This collection of yaml documents will create 4 `PersistentVolume`s
belonging to the `basic` class. Kubernetes will select from this pool
of volumes to satisfy `PersistentVolumeClaim`s from `Pod`s.

Check that the `PersistentVolume`s have been created with:

```
$ oc get pv
NAME      CAPACITY   ACCESS MODES   RECLAIM POLICY   STATUS      CLAIM     STORAGECLASS   REASON    AGE
mdb001    20Gi       RWO            Delete           Available             basic                    2m
mdb002    20Gi       RWO            Delete           Available             basic                    2m
mdb003    20Gi       RWO            Delete           Available             basic                    2m
mdb004    20Gi       RWO            Delete           Available             basic                    2m
```

You can see that the PVs were created and they are in `Available`
status, meaning that they will be bound to a `PersistentVolumeClaim`
when needed. Also the Reclaim Policy has been set to `Delete`, which
means that after a `PVC` has released the `PV`, it will be deleted and
it will be able to be reused.

## Creating a Replica Set With Volumes ##

The final step is to create the `mongodb.ReplicaSet` object to use
these `PersistentVolume`s under the `basic` `StorageClass`. One of
these `ReplicaSet` objects is:

```yaml
---
apiVersion: mongodb.com/v1alpha1
kind: MongoDbReplicaSet
metadata:
  name: dodder
  namespace: mongodb
spec:
  members: 3
  mongodb_version: 3.6.4

  ops_manager_config: global-om-config

  resources:
    cpu: '0.25'
    memory: 512M
    storage: 8G
    storage_class: basic

```

This `MongoDbReplicaset` is requesting 8G for each one of its
replicas. We have 4 PV with 20G each, so this request will be
fullfilled by the existing PVs in the `StorageClass`. We also have to
set the name of the `StorageClass` to use, in this case: `basic`.

Go ahead and create this `MongoDbReplicaSet`. Let's see the state of
the Volumes:

```
$ oc get pv
NAME      CAPACITY   ACCESS MODES   RECLAIM POLICY   STATUS      CLAIM                   STORAGECLASS   REASON    AGE
pv0001    20Gi       RWO            Delete           Bound       mongodb/data-dodder-0   basic                     1m
pv0002    20Gi       RWO            Delete           Bound       mongodb/data-dodder-2   basic                     1m
pv0003    20Gi       RWO            Delete           Bound       mongodb/data-dodder-1   basic                     1m
pv0004    20Gi       RWO            Delete           Available                           basic                     1m
```

3 `PersistentVolumes` are now bound to the claim
`mongodb/data-dodder-x` following the same numbering for the `Pod`s in
the `dodder` `StatefulSet`. Now let's check the
`PersistentVolumeClaim`s:

```
$ oc get pvc
NAME            STATUS    VOLUME    CAPACITY   ACCESS MODES   STORAGECLASS   AGE
data-dodder-0   Bound     pv0001    20Gi       RWO            basic           1m
data-dodder-1   Bound     pv0003    20Gi       RWO            basic           1m
data-dodder-2   Bound     pv0002    20Gi       RWO            basic           1m
```

The `PersistentVolumeClaim` are bound to the `Volume`s `pv0001`,
`pv0002` and `pv0003` following the Kubernetes convention for
`StatefulSet` and `Pod`.

One final thing to note is that each `Pod` requested 8G of storage,
and the `PersistentVolume` had 20GB each, so the `Pod` will be
allocated 20G, and not 8G.
