import pytest
import yaml

import kubernetes
from kubetester.kubetester import KubernetesTester, fixture


@pytest.mark.e2e_standalone_recovery_k8s
class TestStandaloneRecoversBadPvConfiguration(KubernetesTester):
    """
    name: Standalone broken PV configuration
    description: |
      Creates a standalone with a PVC pointing to non-existent storage class and ensures it enters a failed state
      Then the storage class is created and the standalone is expected to reach good state eventually.
      Note that the timeout to reach error state is quite high as we have 3*60=180sec waiting time for Statefulset to reach its
      state after which the controller gives up

    """

    random_storage_name = None

    @classmethod
    def setup_env(cls):
        resource = yaml.safe_load(open(fixture("standalone_pv_invalid.yaml")))

        cls.random_storage_name = KubernetesTester.random_k8s_name()
        resource["spec"]["podSpec"]["persistence"]["single"][
            "storageClass"
        ] = cls.random_storage_name
        cls.create_mongodb_from_object(cls.get_namespace(), resource)
        KubernetesTester.wait_until("in_pending_state", 300)

        mrs = KubernetesTester.get_resource()

        # MDB resource will be stuck in reconciling, waiting for the StatefulSet to reach goal state.
        assert (
            mrs["status"]["message"]
            == "MongoDB my-replica-set-vol-broken resource is reconciling"
        )

    def test_recovery(self):
        resource = yaml.safe_load(open(fixture("test_storage_class.yaml")))
        resource["metadata"]["name"] = self.__class__.random_storage_name
        KubernetesTester.clients("storagev1").create_storage_class(resource)

        print(
            'Created a storage class "{}", standalone is supposed to get fixed now.'.format(
                self.__class__.random_storage_name
            )
        )

        KubernetesTester.wait_until("in_running_state_failures_possible", 300)

    @classmethod
    def teardown_env(cls):
        print(
            '\nRemoving storage class "{}" from Kubernetes'.format(
                cls.random_storage_name
            )
        )
        KubernetesTester.clients("storagev1").delete_storage_class(
            name=cls.random_storage_name, body=kubernetes.client.V1DeleteOptions()
        )
