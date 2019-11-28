import pytest
from kubetester.kubetester import skip_if_local
from kubetester.omtester import OMTester, OMBackgroundTester
from tests.opsmanager.om_base import OpsManagerBase

gen_key_resource_version = None
admin_key_resource_version = None

# Note the strategy for Ops Manager testing: the tests should have more than 1 updates - this is because the initial
# creation of Ops Manager takes too long, so we try to avoid fine-grained test cases and combine different
# updates in one test

# Current test should contain all kinds of scale operations to Ops Manager as a sequence of tests

global om_background_tester


@pytest.mark.e2e_om_ops_manager_scale
class TestOpsManagerCreation(OpsManagerBase):
    """
    name: Ops Manager successful creation
    description: |
      Creates an Ops Manager resource of size 2. There are many configuration options passed to the OM created -
      which allows to bypass the welcome wizard (see conf-hosted-mms-public-template.properties in mms) and get OM
      ready for use
      TODO we need to create a MongoDB resource referencing the OM and check that everything is working during scaling
      operations
    create:
      file: om_ops_manager_scale.yaml
      wait_until: om_in_running_state
      timeout: 1000
    """

    def test_number_of_replicas(self):
        statefulset = self.appsv1.read_namespaced_stateful_set_status(
            self.om_cr.name(), self.namespace
        )
        assert statefulset.status.ready_replicas == 2
        assert statefulset.status.current_replicas == 2

    def test_service(self):
        service = self.corev1.read_namespaced_service(
            self.om_cr.svc_name(), self.namespace
        )
        assert service.spec.type == "ClusterIP"
        assert service.spec.cluster_ip == "None"
        assert len(service.spec.ports) == 1
        assert service.spec.ports[0].target_port == 8080

    def test_endpoints(self):
        """making sure the service points at correct pods"""
        endpoints = self.corev1.read_namespaced_endpoints(
            self.om_cr.svc_name(), self.namespace
        )
        assert len(endpoints.subsets) == 1
        assert len(endpoints.subsets[0].addresses) == 2

    def test_om_resource(self):
        assert self.om_cr.get_om_status_replicas() == 2
        assert self.om_cr.get_om_status_url() == "http://om-scale-svc.{}.svc.cluster.local:8080".format(
            self.namespace
        )

    def test_backup_statefulset(self):
        """ If spec.backup is not specified the backup statefulset is still expected to be created.
         Also the number of replicas doesn't depend on OM replicas """
        statefulset = self.appsv1.read_namespaced_stateful_set_status(
            self.om_cr.backup_sts_name(), self.namespace
        )
        assert statefulset.status.ready_replicas == 1
        assert statefulset.status.current_replicas == 1

    @skip_if_local
    def test_om(self):
        """Checks that the OM is responsive and test service is available (enabled by 'mms.testUtil.enabled')."""
        om_tester = OMTester(self.om_context)
        om_tester.assert_healthiness()
        om_tester.assert_test_service()

        # checking connectivity to each OM instance
        om_tester.assert_om_instances_healthiness(self.om_cr.pod_urls())

    @classmethod
    def teardown_env(cls):
        """Launch the background thread to check OM healthiness during next rolling upgrade"""
        global om_background_tester
        om_tester = OMTester(OpsManagerBase.om_context)
        om_background_tester = OMBackgroundTester(om_tester)
        om_background_tester.start()


@pytest.mark.e2e_om_ops_manager_scale
class TestOpsManagerVersionUpgrade(OpsManagerBase):
    """
    name: Ops Manager image upgrade
    description: |
      The OM version is upgraded - this means the new image is deployed for both OM and appdb.
      The OM upgrade happens in rolling manner, we are checking for OM healthiness in parallel
    update:
      file: om_ops_manager_scale.yaml
      patch: '[{"op":"replace","path":"/spec/version", "value": "4.2.3"}]'
      wait_until: om_in_running_state
      timeout: 1000
    """

    def test_image_url(self):
        """All pods in statefulset are referencing the correct image"""
        pod = self.corev1.read_namespaced_pod("om-scale-0", self.namespace)
        assert "4.2.3" in pod.spec.containers[0].image

        pod = self.corev1.read_namespaced_pod("om-scale-1", self.namespace)
        assert "4.2.3" in pod.spec.containers[0].image

    @skip_if_local
    def test_om_has_been_up_during_upgrade(self):
        om_background_tester.assert_healthiness()


@pytest.mark.e2e_om_ops_manager_scale
class TestOpsManagerScaleUp(OpsManagerBase):
    """
    name: Ops Manager scale up
    description: |
      The OM statefulset is scaled to 3 nodes
    update:
      file: om_ops_manager_scale.yaml
      patch: '[{"op":"replace","path":"/spec/version", "value": "4.2.3"}, {"op":"replace","path":"/spec/replicas", "value": 3}]'
      wait_until: om_in_running_state
      timeout: 500
    """

    def test_number_of_replicas(self):
        statefulset = self.appsv1.read_namespaced_stateful_set_status(
            self.om_cr.name(), self.namespace
        )
        assert statefulset.status.ready_replicas == 3
        assert statefulset.status.current_replicas == 3

        # number of replicas in OM cr has changed as well
        assert self.om_cr.get_om_status_replicas() == 3

    @skip_if_local
    def test_om(self):
        """Checks that the OM is responsive and test service is available (enabled by 'mms.testUtil.enabled')."""
        om_tester = OMTester(self.om_context)
        om_tester.assert_healthiness()
        om_tester.assert_test_service()

        # checking connectivity to each OM instance
        om_tester.assert_om_instances_healthiness(self.om_cr.pod_urls())

        # checking the background thread to make sure the OM was ok during scale up
        om_background_tester.assert_healthiness()


@pytest.mark.e2e_om_ops_manager_scale
class TestOpsManagerScaleDown(OpsManagerBase):
    """
    name: Ops Manager scale down
    description: |
      The OM resource is scaled to 1 node. This is expected to be quite fast and not availability.
      TODO somehow we need to check that termination for OM pods happened successfully: CLOUDP-52310
    update:
      file: om_ops_manager_scale.yaml
      patch: '[{"op":"replace","path":"/spec/version", "value": "4.2.3"}, {"op":"replace","path":"/spec/replicas", "value": 1}]'
      wait_until: om_in_running_state
      timeout: 500
    """

    def test_number_of_replicas(self):
        statefulset = self.appsv1.read_namespaced_stateful_set_status(
            self.om_cr.name(), self.namespace
        )
        assert statefulset.status.ready_replicas == 1
        assert statefulset.status.current_replicas == 1

        # number of replicas in OM cr has changed as well
        assert self.om_cr.get_om_status_replicas() == 1

    @skip_if_local
    def test_om(self):
        """Checks that the OM is responsive"""
        om_tester = OMTester(self.om_context)
        om_tester.assert_healthiness()

        # checking connectivity to a single pod
        om_tester.assert_om_instances_healthiness(self.om_cr.pod_urls())

        # OM was ok during scale down
        om_background_tester.assert_healthiness()
