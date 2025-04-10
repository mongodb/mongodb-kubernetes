# Repro Pod is Killed During a Restore Operation

## Summary

In the middle of an automated database restore, customers found that the Pod was
being killed after 1 hour. They had to manually change the liveness probe to
increase the tolerance to many hours.

It has been confirmed by the automation team that the Agent, when starting a
restore operation, will stop the database and download the restore files, copy
new files to /data and start the mongodb database once again. **During this
period the mongod process is down**, this is, there's no `PID` associated with
it, making the `Livenessprobe` to flag the Pods, causing a Pod restart.

## References

* https://jira.mongodb.org/browse/CLOUDP-84518
* https://jira.mongodb.org/browse/CLOUDP-80199

## Materials

In order to reproduce this scenario we need:

- An Ops Manager instance with Backup configured
- Some way of pushing a reasonable amount of data
  to a Replica set

In this document, we describe how to do both.

# Ops Manager Configuration

The best possible alternative is to run the test `e2e_om_ops_manager_backup`
locally over your own cluster. This cluster should have enough capacity to hold:

+ 1 Ops Manager instance
+ 1 Backup daemon
+ 3 Backup Replica Sets (blockstore, oplog and S3)
+ 1 AppDB Replica Set
+ 1 Test Replica Set (the one we'll fill with data and try to restore)

## Set up Ops Manager with an E2E Test


Just execute the test with the test with the usual:

```shell
make e2e test=e2e_om_ops_manager_backup_manual light=true
```

You'll have to wait around 20 minutes for this to finish.

## Get a Database Dump to push to our new Replica Set

Get the dump from S3 with:

```shell
aws s3 cp s3://cloudp-84518/atlas-sample-database.zip .
```

And unzip:

```shell
unzip atlas-sample-database.zip
```

The dump was obtained from Atlas, after loading a [sample
database](https://docs.atlas.mongodb.com/sample-data/) to it.

## Upload data to the Running Database

This will get a bit hacky, we'll restore the same dump *mutiple times*, into
databases with different names.

1. Start a new Pod and download MongoDB Tools into it:

```
kubectl run mongothings -n mongodb --image ubuntu -i --tty --rm bash
apt update && apt install curl -y
curl -L https://fastdl.mongodb.org/tools/db/mongodb-database-tools-ubuntu2004-x86_64-100.3.1.tgz -o /tmp/tools.tgz
tar xfz /tmp/tools.tgz
```

2. In a different shell, copy dump into Pod (takes like 2 minutes)

```
kubectl cp dump mongodb/mongothings:/tmp
```

3. Now go back to the other Pod and fill up the database. Each 10 iterations will fill the disk with about 3.5G

```
# 10 iterations will be about ~3.5 G
iterations=10
mrestore="/mongodb-database-tools-ubuntu2004-x86_64-100.3.1/bin/mongorestore"
host="rs-non-fixed-0.rs-non-fixed-svc.mongodb.svc.cluster.local"
cd /tmp/dump

for i in $(seq 1 $iterations); do
  mkdir /tmp/dump-$i
  for dir in *; do cp -r $dir "/tmp/dump-$i/$dir-$i"; done

  "${mrestore}" --host "${host}" /tmp/dump-$i

  rm -rf /tmp/dump-$i
done

```


# Backup & Restore

## Enable Continuous Backup

The `rs-fixed` and `rs-non-fixed` will have *Continuous Backup* enabled by this
point. We'll push some data into the database to cause a future restore to make
the database to fail.

10 copies of the Atlas sample database generate a restore of around 7GB, then it
takes more than 3 minutes to `MakeBackupDataAvailable`. Which means that the Pod
will be restarted because of the LivenessProbe.

```
$ kubectl get pods
NAME                                           READY   STATUS      RESTARTS   AGE
rs-non-fixed-0                                     1/1     Running     0          14m
rs-non-fixed-1                                     1/1     Running     0          14m
rs-non-fixed-2                                     1/1     Running     0          14m
```

For this investigation, we use 3 Pods; to make this easier to reproduce, we'll
wait for 60 minutes before starting the tests.

Downloading the Backup file takes a long time (it is several GBs in size), and
this helps with the investigation.

After some time, the Pods that are younger than 60 minutes get to running (and
Alive) state, but not the one that's older than 60 minutes. In order to fix it
I will proceed with the solution in next paragraph.

## Pods keep being restarted

The Pods, because they can't download the full 7GB of restore in less than 3
minutes will be restarted, causing the whole process to start from scratch: the
agent has to be downloaded again, get the automation config, and start
downloading the restore archive.

## How does the LivenessProbe works in this case

The `LivenessProbe` is configured as follows:

```golang
func databaseLivenessProbe() probes.Modification {
	return probes.Apply(
		probes.WithExecCommand([]string{databaseLivenessProbeCommand}),
		probes.WithInitialDelaySeconds(60),
		probes.WithTimeoutSeconds(30),
		probes.WithPeriodSeconds(30),
		probes.WithSuccessThreshold(1),
		probes.WithFailureThreshold(6),
	)
}
```

The `LivenessProbe` has a 60 minutes tolerance to the absence of `mongod`,
`mongos` and `agent` processes. If we wait until the Pods are older than 1 hour,
and because the `LivenessProbe` is configured to fail after 6 tries
(`FailureThreshold`) and to probe every 30 seconds (`PeriodSeconds`), then the
Pods will be flag as failed after ~180 seconds.

When describing one of the Pods we get:

```
Events:
  Type     Reason     Age                      From     Message
  ----     ------     ----                     ----     -------
  Warning  Unhealthy  2m54s (x171 over 3h28m)  kubelet  Readiness probe failed:
  Warning  Unhealthy  2m53s (x10 over 162m)    kubelet  Liveness probe failed:

```

And the Pods will report multiple restarts:

```shell
kubectl get pods
NAME                                           READY   STATUS    RESTARTS   AGE
rs-non-fixed-0                                 0/1     Running   2          6h1m
rs-non-fixed-1                                 0/1     Running   1          3h19m
rs-non0fixed-2                                 0/1     Running   1          79m
```

# Solution

If the `livenessProbe` is modified, to consider the agent process to be running,
as a sufficient condition for the Pod to be considered alive. The restore
operation succeeds after a reasonable amount of time.

In this scenario, 2 MongoDB objects are provided:

* `rs-fixed`: Uses a *modified* version of the `probe.sh` which will accept the
  Agent's PID as sufficient condition for a Pod to be alive.
* `rs-non-fixed`: Uses the *regular* version of `probe.sh`.


`rs-fixed` survives a restore of a 3G snapshot, but `rs-non-fixed` does not.

Both are identical, but `rs-fixed` has been configure with a special
LivenessProbe with the proposed fix, while `rs-non-fixed` is using the
LivenessProbe part of the Operator 1.10.
