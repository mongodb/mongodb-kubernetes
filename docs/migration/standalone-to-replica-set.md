# Migration Guide: MCK Standalone to One Member ReplicaSet

This guide moves the data of a standalone to a new one member `ReplicaSet` in the same namespace: deploy the replica set, copy the data with `mongodump` and `mongorestore`, point applications at the new connection string, delete the standalone.

---

## Prerequisites

- `kubectl` access to the namespace of the standalone, with permission to create `MongoDB`, `MongoDBUser`, `ConfigMap`, `Secret` and `Pod` objects.
- Free disk for the dump archive.

The examples use the namespace `mongodb`, the standalone `my-standalone` and the replica set `my-replica-set`. Adjust the names to your environment.

---

## Migration Steps

### 1. Deploy the replica set

Copy the standalone's Ops Manager project `ConfigMap` to a new one, `my-project-rs` in the examples, keeping `baseUrl` and `orgId` and setting `projectName` to a new unique name. The operator creates that project when the replica set starts, a project holds one deployment and the standalone's project cannot be reused.

Add any spec setting the standalone has that this manifest does not, and keep `type`, `members` and `opsManager.configMapRef` as shown.

```yaml
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: my-replica-set
  namespace: mongodb
spec:
  type: ReplicaSet
  members: 1
  version: 8.0.6-ent            # same as the standalone
  opsManager:
    configMapRef:
      name: my-project-rs       # the new project ConfigMap
  credentials: my-credentials   # same Secret as the standalone
  persistent: true
```

```sh
kubectl apply -f replica-set.yaml
```

✅ Verify that the resource reaches `Running`.

### 2. Start a tools pod

The non static database images do not carry `mongosh`, `mongodump` or `mongorestore`, run the commands from a pod with the enterprise server image matching the standalone version.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: mongodb-tools
  namespace: mongodb
spec:
  containers:
  - name: mongodb-tools
    image: quay.io/mongodb/mongodb-enterprise-server:8.0.6-ubi9   # same version as the standalone
    command: ["sleep", "infinity"]
```

```sh
kubectl apply -f mongodb-tools.yaml
```

✅ Verify that the pod is `Ready` and exec into it. You need the connection string of the standalone and the connection string of the replica set.

### 3. Dump and restore

Stop application writes before the dump and keep them stopped until the applications run against the replica set, a standalone has no oplog and writes made during the dump would be lost.

Hash every user database and note each collection's document count and [dbHash](https://www.mongodb.com/docs/manual/reference/command/dbHash/).

Dump the standalone with `mongodump`, see the [mongodump docs](https://www.mongodb.com/docs/database-tools/mongodump/). Restore the dump into the replica set with `mongorestore`, excluding `admin.*` and `config.*`, see the [mongorestore docs](https://www.mongodb.com/docs/database-tools/mongorestore/). The copy does not include users, recreate the users of the standalone as `MongoDBUser` resources on the replica set, see [Manage Database Users](https://www.mongodb.com/docs/kubernetes/current/manage-users/).

✅ Verify the restore output: N document(s) restored successfully, 0 document(s) failed to restore. Run the same counts and hashes against the replica set and compare with the source notes.

### 4. Clean up

Once you no longer need the rollback option, delete the tools pod, the standalone resource and the source project `ConfigMap` if nothing else uses it. The operator does not delete the persistent volume claim (PVC) of the standalone, delete it yourself once you are sure the data is no longer needed.

---

## Rollback

Until the applications run against the replica set the standalone is untouched, roll back by deleting the replica set resource and its PVC, then re-enable writes. After that, roll back by stopping application writes again and running the same dump and restore in reverse, from the replica set into the standalone. Keep the standalone and its PVC until you are sure.
