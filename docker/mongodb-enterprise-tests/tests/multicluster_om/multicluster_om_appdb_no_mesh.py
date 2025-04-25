import datetime
import json
import time
from typing import List

import kubernetes
import pymongo
import pytest
import yaml
from kubernetes import client
from kubetester import create_or_update_configmap, create_or_update_service, try_load
from kubetester.awss3client import AwsS3Client
from kubetester.certs import (
    create_multi_cluster_mongodb_tls_certs,
    create_ops_manager_tls_certs,
)
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as _fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.conftest import (
    assert_data_got_restored,
    create_appdb_certs,
    get_central_cluster_client,
    get_member_cluster_clients,
    update_coredns_hosts, TELEMETRY_CONFIGMAP_NAME,
)
from tests.multicluster_appdb.conftest import (
    create_s3_bucket_blockstore,
    create_s3_bucket_oplog,
)

from .. import test_logger
from ..common.constants import MEMBER_CLUSTER_1, MEMBER_CLUSTER_2, MEMBER_CLUSTER_3
from ..common.ops_manager.multi_cluster import (
    ops_manager_multi_cluster_with_tls_s3_backups,
)
from ..multicluster.conftest import cluster_spec_list

# This test is for checking networking when OM is deployed without Service Mesh:
#  - with TLS and custom CAs for AppDB and OM
#  - AppDB monitoring enabled
#  - AppDB in MultiCluster mode, but limited to a single member cluster for simplicity
#  - S3 backups enabled
#  - OM's external connectivity enabled

TEST_DATA = {"_id": "unique_id", "name": "John", "address": "Highway 37", "age": 30}

OM_NAME = "om-mc"
OM_CERT_PREFIX = "om-prefix"
APPDB_CERT_PREFIX = "appdb-prefix"

# The hostname "nginx-ext-svc-interconnected" is used as the service name, since it does not allow "." in names.
# The "nginx-ext-svc.interconnected" is required since all hostnames needed to be under the "interconnected" TLD in the CoreDNS configuration.
# There is a CoreDNS configuration below that overwrites requests from "nginx-ext-svc-interconnected" to "nginx-ext-svc.interconnected".
NGINX_EXT_SVC_HOSTNAME = "nginx-ext-svc-interconnected"
NGINX_EXT_SVC_COREDNS_HOSTNAME = "nginx-ext-svc.interconnected"

# The OM external service name are not used by mongodb's or the operator.
# They are only used so that nginx can redirect requests to these hostnames, and we add them in the CoreDNS config.
# This is basically a way for nginx to distribute requests between the 3 load balancer IPs.
OM_1_EXT_SVC_HOSTNAME = "om-mc-1-svc-ext.mongodb-test.interconnected"
OM_2_EXT_SVC_HOSTNAME = "om-mc-2-svc-ext.mongodb-test.interconnected"
OM_3_EXT_SVC_HOSTNAME = "om-mc-3-svc-ext.mongodb-test.interconnected"

OM_DB_0_0_SVC_HOSTNAME = "om-mc-db-0-0.kind-e2e-cluster-1.interconnected"
OM_DB_1_0_SVC_HOSTNAME = "om-mc-db-1-0.kind-e2e-cluster-2.interconnected"
OM_DB_1_1_SVC_HOSTNAME = "om-mc-db-1-1.kind-e2e-cluster-2.interconnected"
OM_DB_2_0_SVC_HOSTNAME = "om-mc-db-2-0.kind-e2e-cluster-3.interconnected"
OM_DB_2_1_SVC_HOSTNAME = "om-mc-db-2-1.kind-e2e-cluster-3.interconnected"
OM_DB_2_2_SVC_HOSTNAME = "om-mc-db-2-2.kind-e2e-cluster-3.interconnected"
MDB_0_0_SVC_HOSTNAME = "multi-cluster-replica-set-0-0.kind-e2e-cluster-1.interconnected"
MDB_1_0_SVC_HOSTNAME = "multi-cluster-replica-set-1-0.kind-e2e-cluster-2.interconnected"
MDB_2_0_SVC_HOSTNAME = "multi-cluster-replica-set-2-0.kind-e2e-cluster-3.interconnected"
CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"

logger = test_logger.get_test_logger(__name__)


@fixture(scope="module")
def ops_manager_certs(namespace: str, multi_cluster_issuer: str):
    additional_domains = [OM_1_EXT_SVC_HOSTNAME, OM_2_EXT_SVC_HOSTNAME, OM_3_EXT_SVC_HOSTNAME, NGINX_EXT_SVC_HOSTNAME]

    return create_ops_manager_tls_certs(
        multi_cluster_issuer,
        namespace,
        OM_NAME,
        secret_name=f"{OM_CERT_PREFIX}-{OM_NAME}-cert",
        additional_domains=additional_domains,
    )


@fixture(scope="module")
def appdb_certs(namespace: str, multi_cluster_issuer: str):
    additional_domains = [
        OM_DB_0_0_SVC_HOSTNAME,
        OM_DB_1_0_SVC_HOSTNAME,
        OM_DB_1_1_SVC_HOSTNAME,
        OM_DB_2_0_SVC_HOSTNAME,
        OM_DB_2_1_SVC_HOSTNAME,
        OM_DB_2_2_SVC_HOSTNAME,
    ]

    return create_appdb_certs(
        namespace,
        multi_cluster_issuer,
        OM_NAME + "-db",
        cluster_index_with_members=[(0, 1), (1, 2), (2, 3)],
        cert_prefix=APPDB_CERT_PREFIX,
        additional_domains=additional_domains,
    )


@fixture(scope="module")
def s3_bucket_blockstore(namespace: str, aws_s3_client: AwsS3Client) -> str:
    return next(create_s3_bucket_blockstore(namespace, aws_s3_client, api_client=get_central_cluster_client()))


@fixture(scope="module")
def s3_bucket_oplog(namespace: str, aws_s3_client: AwsS3Client) -> str:
    return next(create_s3_bucket_oplog(namespace, aws_s3_client, api_client=get_central_cluster_client()))


@mark.e2e_multi_cluster_om_appdb_no_mesh
def test_configure_dns(disable_istio):
    host_mappings = [
        (
            "172.18.255.211",
            NGINX_EXT_SVC_COREDNS_HOSTNAME,
        ),
        (
            "172.18.255.212",
            OM_DB_0_0_SVC_HOSTNAME,
        ),
        (
            "172.18.255.213",
            OM_1_EXT_SVC_HOSTNAME,
        ),
        (
            "172.18.255.214",
            MDB_0_0_SVC_HOSTNAME,
        ),
        (
            "172.18.255.221",
            OM_DB_1_0_SVC_HOSTNAME,
        ),
        (
            "172.18.255.222",
            OM_DB_1_1_SVC_HOSTNAME,
        ),
        (
            "172.18.255.223",
            OM_2_EXT_SVC_HOSTNAME,
        ),
        (
            "172.18.255.224",
            MDB_1_0_SVC_HOSTNAME,
        ),
        (
            "172.18.255.231",
            OM_DB_2_0_SVC_HOSTNAME,
        ),
        (
            "172.18.255.232",
            OM_DB_2_1_SVC_HOSTNAME,
        ),
        (
            "172.18.255.233",
            OM_DB_2_2_SVC_HOSTNAME,
        ),
        (
            "172.18.255.234",
            OM_3_EXT_SVC_HOSTNAME,
        ),
        (
            "172.18.255.235",
            MDB_2_0_SVC_HOSTNAME,
        ),
    ]

    # This rule rewrites nginx-ext-svc-interconnected that ends with *-interconnected to *.interconnected
    # It's useful when kubefwd propagates LoadBalancer IP by service name nginx-ext-svc-interconnected,
    # but for CoreDNS configuration to work it has to end with .interconnected domain (nginx-ext-svc.interconnected)
    rewrite_nginx_hostname = f"rewrite name exact {NGINX_EXT_SVC_HOSTNAME} {NGINX_EXT_SVC_COREDNS_HOSTNAME}"

    for c in get_member_cluster_clients():
        update_coredns_hosts(
            host_mappings=host_mappings,
            api_client=c.api_client,
            cluster_name=c.cluster_name,
            additional_rules=[rewrite_nginx_hostname],
        )


@mark.e2e_multi_cluster_om_appdb_no_mesh
def test_disable_istio(disable_istio):
    logger.info("Istio disabled")


@mark.e2e_multi_cluster_om_appdb_no_mesh
def test_configure_nginx(namespace: str):
    cluster_client = get_central_cluster_client()

    conf = open(_fixture("nginx.conf")).read()
    data = {"nginx.conf": conf}
    create_or_update_configmap(namespace, "nginx-conf", data, api_client=cluster_client)

    nginx_deployment = yaml.safe_load(open(_fixture("nginx.yaml")))
    apps_api = client.AppsV1Api(api_client=cluster_client)
    try:
        apps_api.create_namespaced_deployment(namespace, nginx_deployment)
    except kubernetes.client.ApiException as e:
        if e.status == 409:
            apps_api.replace_namespaced_deployment("nginx", namespace, nginx_deployment)
        else:
            raise Exception(f"failed to create nginx_deployment: {e}")

    nginx_service = yaml.safe_load(open(_fixture("nginx-service.yaml")))
    create_or_update_service(namespace, service=nginx_service)


@fixture(scope="function")
def ops_manager(
    custom_version: str,
    namespace: str,
    custom_appdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
    ops_manager_certs: str,
    appdb_certs: str,
    issuer_ca_filepath: str,
    s3_bucket_blockstore: str,
    s3_bucket_oplog: str,
    multi_cluster_issuer_ca_configmap: str,
) -> MongoDBOpsManager:
    resource = ops_manager_multi_cluster_with_tls_s3_backups(
        namespace, OM_NAME, central_cluster_client, custom_appdb_version, s3_bucket_blockstore, s3_bucket_oplog
    )

    if try_load(resource):
        return resource

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource["spec"]["version"] = custom_version
    resource["spec"]["topology"] = "MultiCluster"
    resource["spec"]["clusterSpecList"] = [
        {
            "clusterName": MEMBER_CLUSTER_1,
            "members": 1,
            "backup": {
                "members": 1,
            },
        },
        {
            "clusterName": MEMBER_CLUSTER_2,
            "members": 1,
            "backup": {
                "members": 1,
            },
        },
        {
            "clusterName": MEMBER_CLUSTER_3,
            "members": 1,
            "backup": {
                "members": 2,
            },
        },
    ]

    resource["spec"]["security"] = {
        "certsSecretPrefix": OM_CERT_PREFIX,
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    resource["spec"]["externalConnectivity"] = {
        "type": "LoadBalancer",
        "port": 9000,
    }
    resource["spec"]["opsManagerURL"] = f"https://{NGINX_EXT_SVC_HOSTNAME}:8180"

    resource["spec"]["applicationDatabase"] = {
        "topology": "MultiCluster",
        "version": custom_appdb_version,
        "agent": {"logLevel": "DEBUG"},
        "security": {
            "certsSecretPrefix": APPDB_CERT_PREFIX,
            "tls": {
                "ca": multi_cluster_issuer_ca_configmap,
            },
        },
        "externalAccess": {"externalDomain": "some.custom.domain"},
    }
    resource["spec"]["applicationDatabase"]["clusterSpecList"] = [
        {
            "clusterName": MEMBER_CLUSTER_1,
            "members": 1,
            "externalAccess": {
                "externalDomain": "kind-e2e-cluster-1.interconnected",
                "externalService": {
                    "spec": {
                        "type": "LoadBalancer",
                        "publishNotReadyAddresses": False,
                        "ports": [
                            {
                                "name": "mongodb",
                                "port": 27017,
                            },
                            {
                                "name": "backup",
                                "port": 27018,
                            },
                            {
                                "name": "testing2",
                                "port": 27019,
                            },
                        ],
                    }
                },
            },
        },
        {
            "clusterName": MEMBER_CLUSTER_2,
            "members": 2,
            "externalAccess": {
                "externalDomain": "kind-e2e-cluster-2.interconnected",
                "externalService": {
                    "spec": {
                        "type": "LoadBalancer",
                        "publishNotReadyAddresses": False,
                        "ports": [
                            {
                                "name": "mongodb",
                                "port": 27017,
                            },
                            {
                                "name": "backup",
                                "port": 27018,
                            },
                            {
                                "name": "testing2",
                                "port": 27019,
                            },
                        ],
                    }
                },
            },
        },
        {
            "clusterName": MEMBER_CLUSTER_3,
            "members": 3,
            "externalAccess": {
                "externalDomain": "kind-e2e-cluster-3.interconnected",
                "externalService": {
                    "spec": {
                        "type": "LoadBalancer",
                        "publishNotReadyAddresses": False,
                        "ports": [
                            {
                                "name": "mongodb",
                                "port": 27017,
                            },
                            {
                                "name": "backup",
                                "port": 27018,
                            },
                            {
                                "name": "testing2",
                                "port": 27019,
                            },
                        ],
                    }
                },
            },
        },
    ]

    return resource


@fixture(scope="function")
def server_certs(
    multi_cluster_issuer: str,
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi,
    )


@fixture(scope="function")
def mongodb_multi_collection(mongodb_multi: MongoDBMulti, ca_path: str):

    tester = mongodb_multi.tester(
        port=27017,
        service_names=[MDB_0_0_SVC_HOSTNAME, MDB_1_0_SVC_HOSTNAME, MDB_2_0_SVC_HOSTNAME],
        external=True,
        use_ssl=True,
        ca_path=ca_path,
    )

    collection = pymongo.MongoClient(tester.cnx_string, **tester.default_opts)["testdb"]

    return collection["testcollection"]


@fixture(scope="function")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names,
    custom_mdb_version: str,
    ops_manager: MongoDBOpsManager,
    multi_cluster_issuer_ca_configmap: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [1, 1, 1])
    resource["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }

    resource.configure_backup(mode="enabled")

    resource["spec"]["externalAccess"] = {}
    resource["spec"]["clusterSpecList"][0]["externalAccess"] = {
        "externalDomain": "kind-e2e-cluster-1.interconnected",
        "externalService": {
            "spec": {
                "type": "LoadBalancer",
                "publishNotReadyAddresses": False,
                "ports": [
                    {
                        "name": "mongodb",
                        "port": 27017,
                    },
                    {
                        "name": "backup",
                        "port": 27018,
                    },
                    {
                        "name": "testing0",
                        "port": 27019,
                    },
                ],
            }
        },
    }
    resource["spec"]["clusterSpecList"][1]["externalAccess"] = {
        "externalDomain": "kind-e2e-cluster-2.interconnected",
        "externalService": {
            "spec": {
                "type": "LoadBalancer",
                "publishNotReadyAddresses": False,
                "ports": [
                    {
                        "name": "mongodb",
                        "port": 27017,
                    },
                    {
                        "name": "backup",
                        "port": 27018,
                    },
                    {
                        "name": "testing1",
                        "port": 27019,
                    },
                ],
            }
        },
    }
    resource["spec"]["clusterSpecList"][2]["externalAccess"] = {
        "externalDomain": "kind-e2e-cluster-3.interconnected",
        "externalService": {
            "spec": {
                "type": "LoadBalancer",
                "publishNotReadyAddresses": False,
                "ports": [
                    {
                        "name": "mongodb",
                        "port": 27017,
                    },
                    {
                        "name": "backup",
                        "port": 27018,
                    },
                    {
                        "name": "testing2",
                        "port": 27019,
                    },
                ],
            }
        },
    }

    create_project_config_map(
        om=ops_manager,
        project_name="mongodb",
        mdb_name=MDB_RESOURCE,
        client=central_cluster_client,
        custom_ca=multi_cluster_issuer_ca_configmap,
    )

    resource.configure(ops_manager, "mongodb", api_client=get_central_cluster_client())

    return resource


def create_project_config_map(om: MongoDBOpsManager, mdb_name, project_name, client, custom_ca):
    name = f"{mdb_name}-config"
    data = {
        "baseUrl": om.om_status().get_url(),
        "projectName": project_name,
        "sslMMSCAConfigMap": custom_ca,
        "orgId": "",
    }

    create_or_update_configmap(om.namespace, name, data, client)


@mark.e2e_multi_cluster_om_appdb_no_mesh
def test_deploy_operator(multi_cluster_operator_with_monitored_appdb: Operator):
    multi_cluster_operator_with_monitored_appdb.assert_is_running()


@mark.e2e_multi_cluster_om_appdb_no_mesh
def test_deploy_ops_manager(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.backup_status().assert_reaches_phase(Phase.Running)
    ops_manager.assert_appdb_monitoring_group_was_created()


@mark.e2e_multi_cluster_om_appdb_no_mesh
def test_create_mongodb_multi(server_certs: str, mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=2400, ignore_errors=True)


@skip_if_local
@mark.e2e_multi_cluster_om_appdb_no_mesh
def test_add_test_data(mongodb_multi_collection):
    max_attempts = 100
    while max_attempts > 0:
        try:
            mongodb_multi_collection.insert_one(TEST_DATA)
            return
        except Exception as e:
            print(e)
            max_attempts -= 1
            time.sleep(6)


@mark.e2e_multi_cluster_om_appdb_no_mesh
def test_mdb_backed_up(ops_manager: MongoDBOpsManager):
    ops_manager.get_om_tester(project_name="mongodb").wait_until_backup_snapshots_are_ready(expected_count=1)


@skip_if_local
@mark.e2e_multi_cluster_om_appdb_no_mesh
def test_change_mdb_data(mongodb_multi_collection):
    now_millis = time_to_millis(datetime.datetime.now(tz=datetime.UTC))
    print("\nCurrent time (millis): {}".format(now_millis))
    time.sleep(30)
    mongodb_multi_collection.insert_one({"foo": "bar"})


@mark.e2e_multi_cluster_om_appdb_no_mesh
def test_pit_restore(ops_manager: MongoDBOpsManager):
    now_millis = time_to_millis(datetime.datetime.now(tz=datetime.UTC))
    print("\nCurrent time (millis): {}".format(now_millis))

    pit_datetime = datetime.datetime.now(tz=datetime.UTC) - datetime.timedelta(seconds=15)
    pit_millis = time_to_millis(pit_datetime)
    print("Restoring back to the moment 15 seconds ago (millis): {}".format(pit_millis))

    ops_manager.get_om_tester(project_name="mongodb").create_restore_job_pit(pit_millis)


@mark.e2e_multi_cluster_om_appdb_no_mesh
def test_mdb_ready(mongodb_multi: MongoDBMulti):
    # Note: that we are not waiting for the restore jobs to get finished as PIT restore jobs get FINISHED status
    # right away.
    # But the agent might still do work on the cluster, so we need to wait for that to happen.
    mongodb_multi.assert_reaches_phase(Phase.Pending)
    mongodb_multi.assert_reaches_phase(Phase.Running)


@skip_if_local
@mark.e2e_multi_cluster_om_appdb_no_mesh
def test_data_got_restored(mongodb_multi_collection):
    assert_data_got_restored(TEST_DATA, mongodb_multi_collection, timeout=600)


def time_to_millis(date_time) -> int:
    epoch = datetime.datetime.fromtimestamp(0, tz=datetime.UTC)
    pit_millis = (date_time - epoch).total_seconds() * 1000
    return pit_millis


@mark.e2e_multi_cluster_om_appdb_no_mesh
def test_telemetry_configmap(namespace: str):
    config = KubernetesTester.read_configmap(namespace, TELEMETRY_CONFIGMAP_NAME)

    try:
        payload_string = config.get("lastSendPayloadDeployments")
        payload = json.loads(payload_string)
        # Perform a rudimentary check
        assert isinstance(payload, list), "payload should be a list"
        assert len(payload) == 2, "payload should not be empty"

        assert payload[0]["properties"]["type"] == "ReplicaSet"
        assert payload[0]["properties"]["externalDomains"] == "ClusterSpecific"
        assert payload[1]["properties"]["type"] == "OpsManager"
        assert payload[1]["properties"]["externalDomains"] == "Mixed"
    except json.JSONDecodeError:
        pytest.fail("payload contains invalid JSON data")
