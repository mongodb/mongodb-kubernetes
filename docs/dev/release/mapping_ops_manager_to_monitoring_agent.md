# Agent image version

The Agent image is maintained in the Community Operator's `release.json` ([link](https://github.com/mongodb/mongodb-kubernetes-operator/blob/master/release.json#L6)).

It's version needs to correspond to the version mentioned in the MMS repo. In order to find out what that version is, you need to:

* Go to the [mms repo](https://github.com/10gen/mms) and select the branch of the X.Y release (`on-prem-X.Y`)
* Navigate to the `conf-hosted.properties` (`/server/conf/conf-hosted.properties`) and find the entry `automation.agent.minimumVersion=<agent-version>`

The obtained version should match the one in the `release.json`.

# Using a non-standard Agent image

In some rare occasions (e.g. regressions or future features), it is required to use a more recent version of the Agent.
In such cases, check the latest promoted Agent version at https://spruce.mongodb.com/commits/mms-promoted-builds

Please be mindful about the future maintainers and put a proper comment, why we're using a specific Agent version in this scenario.

# Mapping Ops Manager Version to Monitoring Agent Version

During the image build time (see [here](https://github.com/10gen/ops-manager-kubernetes/blob/master/docker/mongodb-enterprise-operator/Dockerfile.builder#L26)), the current a mapping between OpsManager and corresponding Agent image version is being
pulled from [here](https://us-east-1.aws.webhooks.mongodb-realm.com/api/client/v2.0/app/kubernetes-version-mappings-aarzq/service/ops_manager_version_to_minimum_agent_version/incoming_webhook/list).
This is a public Realm endpoint that reads the data from an Atlas Cluster we (k8s team) manage.

If a new Agent image is released (or bumped in the Community Operator repo), ensure there's a corresponding mapping in Atlas:

* Go to our [Atlas cluster](https://cloud.mongodb.com/v2/5fb40386308f075a75b2e448#metrics/replicaSet/5ff48dcd230c88424e7f10ec/explorer/mappings/ops_manager_to_agent_version/find) and add a new key-value pair under `mappings.ops_manager_to_agent_version_2`, in the form `X.Y: <agent-version>`
* Save the changes and verify that the new value is visible in the [Realm endpoint](https://us-east-1.aws.webhooks.mongodb-realm.com/api/client/v2.0/app/kubernetes-version-mappings-aarzq/service/ops_manager_version_to_minimum_agent_version/incoming_webhook/list).
