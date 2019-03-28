# Instructions about the process of importing existing Kubernetes 

This is description of the steps that were done to verify and check the migration procedure for replica sets from 
"standalone" to Operator-managed in Kubernetes. Based on this instruction the documentation will be created. 

## How to migrate existing replica sets
The main idea is that the name of the Operator-managed resource must match the special pattern so that the PVC was reused.
The other factor is to have data written to `/data` directory so that the new MongoDB process started by the Operator
could reuse the same data.

## Steps to prove migration process

The main idea below is to emulate non-managed database in Kubernetes cluster. It should be persisted - so the Persistent 
Volume Claim and Persistent Volume must be created. The file structure is close to the default one which is used for 
MongoDB deployments, so data resides in `/data`, mongodb log file - in `/data/mongodb.log`, journal - in `/data/journal`. 
So such file structures must be transparantly get bound to the pods created by the Operator and, the Mongodg processes
must start with the same data
 
1. Create a mongodb replicaset with single persistence:
   
   
```bash
kubectl apply -f samples/replica-set-with-volume.yaml
cat samples/replica-set-with-volume.yaml 
   apiVersion: mongodb.com/v1
   kind: MongoDB
   metadata:
     name: liffey-vol
   spec:
     members: 1
     version: 4.0.4
     project: my-project
     credentials: my-credentials
     type: ReplicaSet
     podSpec:
       persistence:
         single:
           storage: 2G
```
   
2. Add some data to the database
```bash
   # from any of the pods
   kubectl exec -it liffey-vol-0 bash
   mongodb@liffey-vol-0:/$ /var/lib/mongodb-mms-automation/mongodb-linux-x86_64-4.0.4/bin/mongo
   liffey-vol:PRIMARY> use test
   liffey-vol:PRIMARY> db.foo.insertOne({"some": 1})
   {
   	"acknowledged" : true,
   	"insertedId" : ObjectId("5c9a0d6742eb10deb84bb0b5")
   }
   ```

3. Shutdown the database from in OM UI
	
4. Hack the directories to make the deployments look like "manual" ones. The procedure must be repeated for each of the pods
```bash
   kubectl exec -it liffey-vol-0 bash # liffey-vol-1, liffey-vol-2
   # remove the symlink for /data/journal and move the journal data to /data/journal folder (default):
   unlink /data/journal
   mkdir /data/journal
   cp /journal/* /data/journal
   
   # move logs to a default place (/data/mongodb.log) - most of all the customers keep them there
   cp /var/log/mongodb-mms-automation/mongodb.log /data
   
   #save the data about journal size and log content to compare them later:
   mongodb@liffey-vol-0:/$ ls -la /data/journal
   total 307220
   drwxr-sr-x 2 mongodb 2000      4096 Mar 26 11:32 .
   drwxrwsr-x 4 root    2000      4096 Mar 26 11:32 ..
   -rw------- 1 mongodb 2000 104857600 Mar 26 11:32 WiredTigerLog.0000000001
   -rw------- 1 mongodb 2000 104857600 Mar 26 11:32 WiredTigerPreplog.0000000001
   -rw------- 1 mongodb 2000 104857600 Mar 26 11:32 WiredTigerPreplog.0000000002
   
   mongodb@liffey-vol-0:/$ tail /data/mongodb.log 
   2019-03-26T11:31:32.895+0000 I STORAGE  [conn65] shutdown: removing fs lock...
   2019-03-26T11:31:32.895+0000 I CONTROL  [conn65] now exiting
   2019-03-26T11:31:32.895+0000 I CONTROL  [conn65] shutting down with code:0
   ```
   
5. Delete the resource
```bash
   kubectl delete mdb liffey-vol
```
   
6. Create it again
```bash
kubectl apply -f samples/replica-set-with-volume.yaml
```

7. Check the correctness
   - make sure data is still there
   ```bash
   kubectl exec -it liffey-vol-0 bash
   mongodb@liffey-vol-0:/$ /var/lib/mongodb-mms-automation/mongodb-linux-x86_64-4.0.4/bin/mongo
   liffey-vol:PRIMARY> use test
   switched to db test
   liffey-vol:PRIMARY> db.foo.find()
   { "_id" : ObjectId("5c9a0d6742eb10deb84bb0b5"), "some" : 1 }   
    ```
   
   - the log file was rolled
   ```bash
   mongodb@liffey-vol-1:/$ tail /var/log/mongodb-mms-automation/mongodb.log.2019-03-26T11-38-16
   2019-03-26T11:31:32.826+0000 I STORAGE  [conn45] shutdown: removing fs lock...
   2019-03-26T11:31:32.826+0000 I CONTROL  [conn45] now exiting
   2019-03-26T11:31:32.826+0000 I CONTROL  [conn45] shutting down with code:0
   ```
   - the new log file was added and is being actively updated (`/var/log/mongodb-mms-automation/mongodb.log`)
   - the content of journal is the same (was copied from /data/journal) as there were no updates done
   ```bash
   mongodb@liffey-vol-0:/$ ls -la /journal
   total 307208
   drwxrwsr-x 2 root    2000      4096 Mar 26 11:39 .
   drwxr-xr-x 1 root    root      4096 Mar 26 11:37 ..
   -rw------- 1 mongodb 2000 104857600 Mar 26 11:44 WiredTigerLog.0000000002
   -rw------- 1 mongodb 2000 104857600 Mar 26 11:38 WiredTigerPreplog.0000000001
   -rw------- 1 mongodb 2000 104857600 Mar 26 11:38 WiredTigerPreplog.0000000002
    ```

