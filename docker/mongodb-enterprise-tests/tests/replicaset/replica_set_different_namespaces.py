import pytest
import string
import random
import yaml
from kubetester.kubetester import KubernetesTester, fixture

@pytest.mark.e2e_replica_set_different_namespaces
class TestReplicaSetWithSecretAndConfigMapInDifferentNamespace(KubernetesTester):
    """
    name: Replica set creation with secret and configmap in different namespace
    tags: replica-set, persistent-volumes, creation
    """
    @classmethod
    def setup_env(cls):
        cls.other_namespace = 'test-ns-' + ''.join(
            random.choice(string.ascii_lowercase) for _ in range(10)
        )
        cls.create_namespace(cls.other_namespace)

        # create secret and config map in different namespace
        project_name = 'test-project'
        creds_name = 'test-creds'
        try:
            org_id = cls.get_om_org_id()
        except ValueError:
            org_id = ""
        cls.create_secret(cls.other_namespace, creds_name, {
            "publicApiKey": cls.get_om_api_key(),
            "user": cls.get_om_user(),
        })
        cls.create_config_map(cls.other_namespace, project_name, {
            "projectName": cls.get_om_group_name(),
            "baseUrl": cls.get_om_base_url(),
            "orgId": org_id,
        })

        # create replica set and wait for it to be ready
        with open(fixture("replica-set.yaml"), "r") as f:
            resource = yaml.safe_load(f)
        resource["spec"]["project"] = "{}/{}".format(cls.other_namespace, project_name)
        resource["spec"]["credentials"] = "{}/{}".format(cls.other_namespace, creds_name)
        cls.create_mongodb_from_object(cls.get_namespace(), resource)
        cls.wait_until("in_running_state", 200)

    @classmethod
    def teardown_env(cls):
        cls.delete_namespace(cls.other_namespace)

    def get_host_strings(self, namespace, name, members):
        """Get hostnames for replicas in a replica set in a given namespace."""
        return ["{}-{}.{}-svc.{}.svc.cluster.local".format(name, n, name, namespace) for n in range(members)]

    def test_can_connect_to_repl_set(self):
        hosts = self.get_host_strings(self.get_namespace(), "my-replica-set", 3)
        primary, secondaries = self.wait_for_rs_is_ready(hosts)

        assert primary is not None
        assert len(secondaries) == 2
