"""
Shared e2e flow verifying that the operator's OTLP exporter forwards MCK deployment
mappings to Ops Manager, which stores them in mmsdbconfig.mckDeploymentMappings
(one document per project: {_id: ObjectId(groupId), mappings: [{mckDeploymentUid,
deploymentName, deploymentUuid}]}).

Each test module subclasses DeploymentMappingsLifecycle for one topology.
"""

import time
from typing import Optional

import pymongo
import semver
from bson import ObjectId
from kubernetes.client.exceptions import ApiException
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMTester
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import get_custom_appdb_version, get_custom_om_version, get_default_operator

MAPPINGS_DATABASE = "mmsdbconfig"
MAPPINGS_COLLECTION = "mckDeploymentMappings"

# The operator exports every 60s; OM resolves deploymentUuid only after monitoring discovers the deployment.
MAPPING_TIMEOUT = 600
# Long enough to cover several export intervals.
ABSENCE_DURATION = 180


def om_supports_deployment_mappings() -> bool:
    try:
        return semver.VersionInfo.parse(get_custom_om_version()).match(">=8.0.27")
    except ValueError:
        return False


def project_id_of(mdb: MongoDB) -> str:
    project_id = mdb.get_om_tester().context.project_id
    assert project_id, f"{mdb.name} has no Ops Manager project id"
    return project_id


def read_mapping_doc(om: MongoDBOpsManager, project_id: str) -> Optional[dict]:
    with pymongo.MongoClient(om.read_appdb_connection_url()) as client:
        return client[MAPPINGS_DATABASE][MAPPINGS_COLLECTION].find_one({"_id": ObjectId(project_id)})


def delete_mapping_doc(om: MongoDBOpsManager, project_id: str):
    with pymongo.MongoClient(om.read_appdb_connection_url()) as client:
        client[MAPPINGS_DATABASE][MAPPINGS_COLLECTION].delete_one({"_id": ObjectId(project_id)})


def wait_for_mapping(
    om: MongoDBOpsManager, mdb: MongoDB, deployment_name: str, deployment_uuid, timeout: int = MAPPING_TIMEOUT
):
    """Waits until the project's document holds exactly one mapping for mdb with the OM-resolved deploymentUuid.

    deployment_uuid is a callable (OMTester) -> Optional[str] re-evaluated on every poll,
    because OM assigns the id only after monitoring discovers the deployment."""
    mdb.load()
    tester = mdb.get_om_tester()
    project_id = project_id_of(mdb)
    uid = mdb["metadata"]["uid"]

    def mapping_matches():
        doc = read_mapping_doc(om, project_id)
        if doc is None:
            return False, f"no {MAPPINGS_COLLECTION} document for project {project_id}"
        expected_uuid = deployment_uuid(tester)
        if expected_uuid is None:
            return False, f"OM has not discovered deployment {deployment_name} yet"
        expected = [{"mckDeploymentUid": uid, "deploymentName": deployment_name, "deploymentUuid": expected_uuid}]
        actual = doc.get("mappings")
        return actual == expected, f"mappings={actual}, expected={expected}"

    run_periodically(
        mapping_matches, timeout=timeout, sleep_time=10, msg=f"deployment mapping for {mdb.name} in Ops Manager"
    )


def assert_mapping_doc_stays_absent(om: MongoDBOpsManager, project_id: str, duration: int = ABSENCE_DURATION):
    deadline = time.time() + duration
    while time.time() < deadline:
        doc = read_mapping_doc(om, project_id)
        assert doc is None, f"exporter still sends mappings for project {project_id}: {doc}"
        time.sleep(10)


def replica_set_cluster_id(tester: OMTester, name: str) -> Optional[str]:
    for cluster in tester.api_get_clusters().get("results", []):
        if cluster.get("typeName") == "REPLICA_SET" and cluster.get("replicaSetName") == name:
            if not cluster.get("shardName"):
                return cluster["id"]
    return None


def sharded_cluster_id(tester: OMTester, name: str) -> Optional[str]:
    for cluster in tester.api_get_clusters().get("results", []):
        if cluster.get("typeName") == "SHARDED_REPLICA_SET" and cluster.get("clusterName") == name:
            return cluster["id"]
    return None


def standalone_host_id(tester: OMTester, hostname: str) -> Optional[str]:
    for host in tester.api_get_hosts().get("results", []):
        if host.get("hostname") == hostname:
            return host["id"]
    return None


@mark.skipif(not om_supports_deployment_mappings(), reason="Ops Manager < 8.0.27 does not store deployment mappings")
class DeploymentMappingsLifecycle:
    """Test sequence shared by all topologies. Subclasses implement build, deployment_name and deployment_uuid."""

    running_timeout = 600

    def build(self, ops_manager: MongoDBOpsManager, namespace: str, mdb_version: str) -> MongoDB:
        raise NotImplementedError

    def deployment_name(self, mdb: MongoDB) -> str:
        raise NotImplementedError

    def deployment_uuid(self, tester: OMTester, mdb: MongoDB) -> Optional[str]:
        raise NotImplementedError

    @fixture(scope="class")
    def operator(self, namespace: str, operator_installation_config: dict[str, str]) -> Operator:
        return get_default_operator(namespace, operator_installation_config)

    @fixture(scope="class")
    def ops_manager(self, namespace: str) -> MongoDBOpsManager:
        resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace)
        resource.set_version(get_custom_om_version())
        resource.set_appdb_version(get_custom_appdb_version())
        try_load(resource)
        return resource

    @fixture(scope="class")
    def mdb(self, ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str) -> MongoDB:
        resource = self.build(ops_manager, namespace, custom_mdb_version)
        try_load(resource)
        return resource

    def _wait_for_mapping(self, ops_manager: MongoDBOpsManager, mdb: MongoDB):
        wait_for_mapping(ops_manager, mdb, self.deployment_name(mdb), lambda tester: self.deployment_uuid(tester, mdb))

    def test_install_operator(self, operator: Operator):
        operator.wait_for_operator_ready()

    def test_create_ops_manager(self, ops_manager: MongoDBOpsManager):
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_create_mdb(self, mdb: MongoDB):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=self.running_timeout)

    def test_mapping_recorded(self, ops_manager: MongoDBOpsManager, mdb: MongoDB):
        self._wait_for_mapping(ops_manager, mdb)

    def test_mapping_exported_after_operator_restart(
        self, ops_manager: MongoDBOpsManager, mdb: MongoDB, operator: Operator
    ):
        operator.restart_operator_deployment()
        # Deleted only after the restart so that a re-created document proves the new operator pod exports.
        delete_mapping_doc(ops_manager, project_id_of(mdb))
        self._wait_for_mapping(ops_manager, mdb)

    def test_export_stops_after_mdb_deleted(self, ops_manager: MongoDBOpsManager, mdb: MongoDB):
        project_id = project_id_of(mdb)
        mdb.delete()

        def mdb_is_deleted() -> bool:
            try:
                mdb.load()
                return False
            except ApiException as e:
                return e.status == 404

        run_periodically(mdb_is_deleted, timeout=600, msg=f"{mdb.name} to be deleted")
        # Let an export already in flight when the exporter was released land before clearing the document.
        time.sleep(15)
        delete_mapping_doc(ops_manager, project_id)
        assert_mapping_doc_stays_absent(ops_manager, project_id)

    def test_recreated_mdb_mapped_to_new_uid(
        self, ops_manager: MongoDBOpsManager, mdb: MongoDB, namespace: str, custom_mdb_version: str
    ):
        old_uid = mdb["metadata"]["uid"]
        recreated = self.build(ops_manager, namespace, custom_mdb_version)
        recreated.update()
        recreated.assert_reaches_phase(Phase.Running, timeout=self.running_timeout)

        assert recreated["metadata"]["uid"] != old_uid
        self._wait_for_mapping(ops_manager, recreated)
