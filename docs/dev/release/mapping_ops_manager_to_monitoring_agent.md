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

During the image build time (see [here](https://github.com/10gen/ops-manager-kubernetes/blob/master/docker/mongodb-enterprise-operator/Dockerfile.builder#L30)), 
the current mapping between OpsManager and corresponding Agent image version is being pulled from 'release.json'.

If a new Agent image is released (or bumped in the Community Operator repo), ensure there's a corresponding mapping in `supportedImages.mongodb-agent.opsManagerMapping`.
Make sure you update the `Description for specific versions` as well, so that the future maintainers know why certain upgrades have been made.
