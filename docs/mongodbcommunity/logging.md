# Configure Logging in MongoDB Community

This section describes the components which are logging either to a file or stdout,
how to configure them and what their defaults are.

## MongoDB Processes
### Configuration
The exposed CRD options can be seen [in the crd yaml](https://github.com/mongodb/mongodb-kubernetes-operator/blob/74d13f189566574b862e5670b366b61ec5b65923/config/crd/bases/mongodbcommunity.mongodb.com_mongodbcommunity.yaml#L105-L117).
Additionally, more information regarding configuring systemLog can be found [in the official documentation of systemLog](https://www.mongodb.com/docs/manual/reference/configuration-options/#core-options)].
`spec.agent.systemLog.destination` configures the logging destination of the mongod process.
### Default Values
By default, MongoDB sends all log output to standard output.

## MongoDB Agent
### Configuration
`spec.agent.logFile` can be used to configure the output file of the mongoDB agent logging.
The agent will log to standard output with the following setting: `/dev/stdout`.
### Default Values
By default, the MongoDB agent logs to `/var/log/mongodb-mms-automation/automation-agent.log`

## ReadinessProbe
### Configuration & Default Values
The readiness probe binary reads the environment variables below from its own container, the
`mongodb-agent` container (the container that runs the probe). When a variable is unset the
readiness probe uses the default in the table.

| Environment Variable            | Explanation                                                             | Default Value                                 |
|---------------------------------|-------------------------------------------------------------------------|-----------------------------------------------|
| READINESS_PROBE_LOGGER_BACKUPS  | maximum number of old log files to retain                               | 5                                             |
| READINESS_PROBE_LOGGER_MAX_SIZE | maximum size in megabytes                                               | 5                                             |
| READINESS_PROBE_LOGGER_MAX_AGE  | maximum number of days to retain old log files                          | none                                          |
| READINESS_PROBE_LOGGER_COMPRESS | if the rotated log files should be compressed                           | false                                         |
| MDB_WITH_AGENT_FILE_LOGGING     | whether the readiness probe also writes its log to a file (in addition to stdout) | true                                |
| LOG_FILE_PATH                   | path of the logfile of the readinessProbe.                              | /var/log/mongodb-mms-automation/readiness.log |

To override any of these defaults, set the environment variable on the `mongodb-agent` container
through the `spec.statefulSet` override, which the operator merges (by container name) into the
StatefulSet it creates. The `MongoDBCommunity` resource has no dedicated readiness-probe
configuration field for this.

```yaml
apiVersion: mongodbcommunity.mongodb.com/v1
kind: MongoDBCommunity
metadata:
  name: example-mongodb
spec:
  members: 3
  type: ReplicaSet
  version: "8.0.12"
  statefulSet:
    spec:
      template:
        spec:
          containers:
            - name: mongodb-agent
              env:
                - name: READINESS_PROBE_LOGGER_MAX_SIZE
                  value: "50"
                - name: READINESS_PROBE_LOGGER_BACKUPS
                  value: "10"
                - name: MDB_WITH_AGENT_FILE_LOGGING
                  value: "false"
  # ...
```
