import pytest
from kubernetes import client
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.omtester import OMBackgroundTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.conftest import (
    get_member_cluster_api_client,
    is_multi_cluster,
    verify_pvc_expanded,
)
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

gen_key_resource_version = None
admin_key_resource_version = None

# Note the strategy for Ops Manager testing: the tests should have more than 1 updates - this is because the initial
# creation of Ops Manager takes too long, so we try to avoid fine-grained test cases and combine different
# updates in one test

# Current test should contain all kinds of scale operations to Ops Manager as a sequence of tests
RESIZED_STORAGE_SIZE = "2Gi"


@fixture(scope="module")
def ops_manager(
    namespace,
    custom_version: str,
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_scale.yaml"), namespace=namespace)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource, om_cluster_spec_list=[2, 1, 1])

    try_load(resource)
    return resource


@fixture(scope="module")
def background_tester(ops_manager: MongoDBOpsManager) -> OMBackgroundTester:
    om_background_tester = OMBackgroundTester(ops_manager.get_om_tester())
    om_background_tester.start()
    return om_background_tester


@pytest.mark.e2e_om_ops_manager_scale
class TestOpsManagerCreation:
    """
    Creates an Ops Manager resource of size 2. There are many configuration options passed to the OM created -
    which allows to bypass the welcome wizard (see conf-hosted-mms-public-template.properties in mms) and get OM
    ready for use
    TODO we need to create a MongoDB resource referencing the OM and check that everything is working during scaling
    operations
    """

    def test_create_om(self, ops_manager: MongoDBOpsManager):
        # Backup is not fully configured so we wait until Pending phase

        ops_manager.update()
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            timeout=900,
            msg_regexp="Oplog Store configuration is required for backup.*",
        )

    def test_number_of_replicas(self, ops_manager: MongoDBOpsManager):
        for member_cluster_name in ops_manager.get_om_member_cluster_names():
            statefulset = ops_manager.read_statefulset(member_cluster_name=member_cluster_name)
            replicas = ops_manager.get_om_replicas_in_member_cluster(member_cluster_name=member_cluster_name)
            assert statefulset.status.ready_replicas == replicas
            assert statefulset.status.current_replicas == replicas

    def test_service(self, ops_manager: MongoDBOpsManager):
        for _, cluster_spec_item in ops_manager.get_om_indexed_cluster_spec_items():
            internal, external = ops_manager.services(cluster_spec_item["clusterName"])
            assert external is None
            assert internal.spec.type == "ClusterIP"
            if not ops_manager.is_om_multi_cluster():
                assert internal.spec.cluster_ip == "None"
            assert len(internal.spec.ports) == 2
            assert internal.spec.ports[0].target_port == 8080
            assert internal.spec.ports[1].target_port == 25999

    def test_endpoints(self, ops_manager: MongoDBOpsManager):
        """making sure the service points at correct pods"""
        for member_cluster_name in ops_manager.get_om_member_cluster_names():
            endpoints = client.CoreV1Api(
                api_client=get_member_cluster_api_client(member_cluster_name)
            ).read_namespaced_endpoints(ops_manager.svc_name(member_cluster_name), ops_manager.namespace)
            replicas = ops_manager.get_om_replicas_in_member_cluster(member_cluster_name)
            assert len(endpoints.subsets) == 1
            assert len(endpoints.subsets[0].addresses) == replicas

    def test_om_resource(self, ops_manager: MongoDBOpsManager):
        assert ops_manager.om_status().get_replicas() == ops_manager.get_total_number_of_om_replicas()
        assert ops_manager.om_status().get_url() == "http://om-scale-svc.{}.svc.cluster.local:8080".format(
            ops_manager.namespace
        )

    def test_backup_pod(self, ops_manager: MongoDBOpsManager):
        """If spec.backup is not specified the backup statefulset is still expected to be created.
        Also the number of replicas doesn't depend on OM replicas. The backup daemon pod will become
        ready when the web server becomes available.
        """
        ops_manager.wait_until_backup_pods_become_ready()

    @skip_if_local
    def test_om(self, ops_manager: MongoDBOpsManager):
        """Checks that the OM is responsive and test service is available (enabled by 'mms.testUtil.enabled')."""
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        om_tester.assert_test_service()

        # checking connectivity to each OM instance
        # We can only perform this test in the single cluster case because we don't create a service per pod so that
        # every OM pod can be addressable by FQDN from a different cluster (the test pod cluster for example).
        if not is_multi_cluster():
            om_tester.assert_om_instances_healthiness(ops_manager.pod_urls())


@pytest.mark.e2e_om_ops_manager_scale
class TestOpsManagerPVCExpansion:

    def test_expand_pvc(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["applicationDatabase"]["podSpec"]["persistence"]["multiple"]["data"][
            "storage"
        ] = RESIZED_STORAGE_SIZE
        ops_manager["spec"]["applicationDatabase"]["podSpec"]["persistence"]["multiple"]["journal"][
            "storage"
        ] = RESIZED_STORAGE_SIZE
        ops_manager.update()

    def test_appdb_ready_after_expansion(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_appdb_expansion_finished(self, ops_manager: MongoDBOpsManager, namespace: str):
        for member_cluster_name in ops_manager.get_appdb_member_cluster_names():
            sts = ops_manager.read_appdb_statefulset(member_cluster_name=member_cluster_name)
            assert sts.spec.volume_claim_templates[0].spec.resources.requests["storage"] == RESIZED_STORAGE_SIZE


@pytest.mark.e2e_om_ops_manager_scale
class TestOpsManagerVersionUpgrade:
    """
    The OM version is upgraded - this means the new image is deployed for both OM, appdb and backup.
    The OM upgrade happens in rolling manner, we are checking for OM healthiness in parallel
    """

    def test_upgrade_om(
        self,
        ops_manager: MongoDBOpsManager,
        background_tester: OMBackgroundTester,
        custom_version: str,
    ):
        # Adding fixture just to start background tester
        _ = background_tester
        ops_manager.load()

        # This updates OM by a major version. For instance:
        # If running OM6 tests, this will update from 5.0.9 to latest OM6
        ops_manager.set_version(custom_version)

        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_image_url(self, ops_manager: MongoDBOpsManager):
        """All pods in statefulset are referencing the correct image"""
        pods = ops_manager.read_om_pods()
        assert len(pods) == ops_manager.get_total_number_of_om_replicas()
        for _, pod in pods:
            assert ops_manager.get_version() in pod.spec.containers[0].image

    @skip_if_local
    def test_om_has_been_up_during_upgrade(self, background_tester: OMBackgroundTester):

        # 10% of the requests are allowed to fail
        background_tester.assert_healthiness(allowed_rate_of_failure=0.1)


@pytest.mark.e2e_om_ops_manager_scale
class TestOpsManagerScaleUp:
    """
    The OM statefulset is scaled to 3 nodes
    """

    def test_scale_up_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        if is_multi_cluster():
            enable_multi_cluster_deployment(ops_manager, om_cluster_spec_list=[3, 2, 1])
        else:
            ops_manager["spec"]["replicas"] = 3
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1000)

    def test_number_of_replicas(self, ops_manager: MongoDBOpsManager):
        for member_cluster_name in ops_manager.get_om_member_cluster_names():

            statefulset = ops_manager.read_statefulset(member_cluster_name=member_cluster_name)
            replicas = ops_manager.get_om_replicas_in_member_cluster(member_cluster_name=member_cluster_name)
            assert statefulset.status.ready_replicas == replicas
            assert statefulset.status.current_replicas == replicas

        assert ops_manager.om_status().get_replicas() == ops_manager.get_total_number_of_om_replicas()

    @skip_if_local
    def test_om(self, ops_manager: MongoDBOpsManager, background_tester: OMBackgroundTester):
        """Checks that the OM is responsive and test service is available (enabled by 'mms.testUtil.enabled')."""
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        om_tester.assert_test_service()

        # checking connectivity to each OM instance.
        # We can only perform this test in the single cluster case because we don't create a service per pod so that
        # every OM pod can be addressable by FQDN from a different cluster (the test pod cluster for example).
        if not is_multi_cluster():
            om_tester.assert_om_instances_healthiness(ops_manager.pod_urls())

        # checking the background thread to make sure the OM was ok during scale up
        background_tester.assert_healthiness(allowed_rate_of_failure=0.1)


@pytest.mark.e2e_om_ops_manager_scale
class TestOpsManagerScaleDown:
    """
    The OM resource is scaled to 1 node. This is expected to be quite fast and not availability.
    TODO somehow we need to check that termination for OM pods happened successfully: CLOUDP-52310
    """

    def test_scale_down_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        if is_multi_cluster():
            enable_multi_cluster_deployment(ops_manager, om_cluster_spec_list=[1, 1, 1])
        else:
            ops_manager["spec"]["replicas"] = 1
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1000)

    def test_number_of_replicas(self, ops_manager: MongoDBOpsManager):
        for member_cluster_name in ops_manager.get_om_member_cluster_names():

            statefulset = ops_manager.read_statefulset(member_cluster_name=member_cluster_name)
            replicas = ops_manager.get_om_replicas_in_member_cluster(member_cluster_name=member_cluster_name)
            if replicas != 0:
                assert statefulset.status.ready_replicas == replicas
                assert statefulset.status.current_replicas == replicas

        assert ops_manager.om_status().get_replicas() == ops_manager.get_total_number_of_om_replicas()

    @skip_if_local
    def test_om(self, ops_manager: MongoDBOpsManager, background_tester: OMBackgroundTester):
        """Checks that the OM is responsive"""
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()

        # checking connectivity to a single pod
        # We can only perform this test in the single cluster case because we don't create a service per pod so that
        # every OM pod can be addressable by FQDN from a different cluster (the test pod cluster for example).
        if not is_multi_cluster():
            om_tester.assert_om_instances_healthiness(ops_manager.pod_urls())

        # OM was ok during scale down
        background_tester.assert_healthiness(allowed_rate_of_failure=0.1)
