# Mapping Ops Manager Version to Monitoring Agent Version

The operator, when built, bundles a mapping between each minor version of OM (4.2,4.2,5.0, etc) and the agent version the operator will configure for the AppDB.

This is the version we use as default monitoring agent for the AppDB.

The operator reads the mapping from [here](https://us-east-1.aws.webhooks.mongodb-realm.com/api/client/v2.0/app/kubernetes-version-mappings-aarzq/service/ops_manager_version_to_minimum_agent_version/incoming_webhook/list). This is a public Realm endpoint that reads the data from an Atlas Cluster we (k8s team) manage.

# Updating the mapping
The update of the mapping needs to be done only when releasing the first OM version of a new minor (approximately once a year).

If you are releasing the first X.Y version of OM, do the following:

* Go to the [mms repo](https://github.com/10gen/mms) and select the branch of the X.Y release (`on-prem-X.Y`)
* Navigate to the `conf-hosted.properties` (`/server/conf/conf-hosted.properties`) and find the entry `automation.agent.minimumVersion=<agent-version>`
* Go to our [Atlas cluster](https://cloud.mongodb.com/v2/5fb40386308f075a75b2e448#metrics/replicaSet/5ff48dcd230c88424e7f10ec/explorer/mappings/ops_manager_to_agent_version/find) and add a new key-value pair under `mappings.ops_manager_to_agent_version`, in the form `X.Y: <agent-version>`
* Save the changes and verify that the new value is visible in the [Realm endpoint](https://us-east-1.aws.webhooks.mongodb-realm.com/api/client/v2.0/app/kubernetes-version-mappings-aarzq/service/ops_manager_version_to_minimum_agent_version/incoming_webhook/list).
