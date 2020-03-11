> Motivation: handling SIGTERM signals from containers is challenging and required a lot of work so it makes sense to share
it with all the team and leave this knowledge for longer

## Problems

### Problem 1: the main process doesn't end on SIGTERM if there are 
Consider the following script which we start using `bash` **locally**:

```bash
#!/usr/bin/env bash
...
tail -F logs
```

Sending the SIGTERM signal to the process will shutdown the process (though `tail` process wil still be running
in background). Though this doesn't work **in case of Kubernetes** and the main script which is hanging on `tail` will still
be running until the container is eventually killed after `terminationGracefulPeriod` passes
*(Disclaimer: I'm not sure about this behavior and why this happens in container, may be this is some feature of process 
being the main one with pid 1)*

### Problem 2: passing SIGTERM to subprocesses
Another serious problem is that the subprocesses (OM/BackupDaemon/Agent+MongoDB) should react on SIGTERM as well and
perform some shutdown logic. For MongoDB for example starting from some version the process will call `rs.stepDown` making
sure that all the pending data is replicated to secondaries in case if this is a PRIMARY member.

### Solution

Solution to both problems is correct handling of signals:
1. make sure the script doesn't ignore the signal and stops its work as soon as SIGTERM is received
2. (optionally) pass the signals to subprocesses letting them handle the signal.

The last statement is optional as the same can be achieved by specifying the `preStop` hook for container. So this 
hook may execute some script which will perform the graceful shutdown for subprocesses. Though it's still critical 
to ensure step 1) as the main process (with pid 1) may still hang around until it's killed if it doesn't react on SIGTERM
properly.

### Implementation (MongoDBOpsManager)

Both `preStop` hook and `trap` command in the script are used to handle shutdown. So the logic is:
1. `preStop` hook is called and either `mongodb-mms stop_mms` or `mongodb-mms stop_backup_daemon` is called resulting 
in graceful shutdown for processes (note, that until [CLOUDP-58336](https://jira.mongodb.org/browse/CLOUDP-58336) is done the timeout for
graceful shutdown is 20 seconds for Ops Manager process and infinity for the Backup Daemon one)
2. `SIGTERM` signal is sent to the script which catches it and passes to the `tail` command run in background

### Implementation (MongoDB)

All the handling is done inside the `agent_launcher` script: the signal is caught and passed to the Agent and Database 
processes, waiting until the processes end.


 
