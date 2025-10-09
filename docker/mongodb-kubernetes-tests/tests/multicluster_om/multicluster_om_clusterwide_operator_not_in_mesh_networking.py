from typing import Optional

import kubernetes
from kubernetes.client.rest import ApiException
from kubetester import read_secret, read_service, try_load
from kubetester.certs import create_ops_manager_tls_certs
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.cert.cert_issuer import create_appdb_certs
from tests.common.constants import MEMBER_CLUSTER_2, MEMBER_CLUSTER_3
from tests.common.ops_manager.multi_cluster import (
    ops_manager_multi_cluster_with_tls_s3_backups,
)
from tests.conftest import (
    create_issuer_ca_configmap,
    get_aws_s3_client,
    get_central_cluster_client,
    get_central_cluster_name,
    get_custom_appdb_version,
    get_custom_om_version,
    get_evergreen_task_id,
    get_issuer_ca_filepath,
    get_member_cluster_api_client,
    get_member_cluster_clients,
    get_multi_cluster_operator_installation_config,
    get_namespace,
    install_multi_cluster_operator_cluster_scoped,
    update_coredns_hosts,
)
from tests.multicluster import prepare_multi_cluster_namespaces
from tests.multicluster.conftest import cluster_spec_list, create_namespace
from tests.multicluster_appdb.conftest import (
    create_s3_bucket_blockstore,
    create_s3_bucket_oplog,
)

# This test requires a cluster-wide operator.
# To run it locally you must specify the following in private-context:
#  WATCH_NAMESPACE="mdb-om-mc,$NAMESPACE"
# When running in EVG or the operator is in a pod (LOCAL_OPERATOR=false) then it's handled automatically.

# This test is for checking networking when:
#  - OM deployed in different namespace than the operator
#  - OM deployed in different clusters than the operator
#  - The operator is not in the same Service Mesh as member clusters (headless service fqdn is not accessible)
#  - TLS enabled with custom CAs for AppDB and OM
#  - AppDB in MultiCluster mode, but limited to a single member cluster for simplicity
#  - S3 backups enabled
#  - OM's external connectivity enabled
#
# Test procedure:
#  - deploy AppDB and OM
#  - wait until the operator failed connecting to deployed OM API endpoint
#  - configure external connectivity and point spec.opsManagerURL to external service's external IP using .interconnected hostname


class MultiClusterOMClusterWideTestHelper:
    OM_NAMESPACE = "mdb-om-mc"
    OM_NAME = "om-mc"
    OM_CERT_PREFIX = "om-prefix"
    APPDB_CERT_PREFIX = "appdb-prefix"
    APPDB_CLUSTER_INDEX_WITH_MEMBERS = [(0, 3)]

    om_issuer_ca_configmap: str
    appdb_issuer_ca_configmap: str
    ops_manager_cert_secret_name: str
    appdb_cert_secret_prefix: str
    s3_bucket_blockstore: str
    s3_bucket_oplog: str

    def prepare_namespaces(self):
        print("MultiClusterOMClusterWideTestHelper: prepare_namespaces")
        image_pull_secret_name = get_multi_cluster_operator_installation_config(get_namespace())[
            "registry.imagePullSecrets"
        ]
        image_pull_secret_data = read_secret(
            get_namespace(), image_pull_secret_name, api_client=get_central_cluster_client()
        )

        create_namespace(
            get_central_cluster_client(),
            get_member_cluster_clients(),
            get_evergreen_task_id(),
            self.OM_NAMESPACE,
            image_pull_secret_name,
            image_pull_secret_data,
        )

        prepare_multi_cluster_namespaces(
            self.OM_NAMESPACE,
            get_multi_cluster_operator_installation_config(get_namespace()),
            get_member_cluster_clients(),
            get_central_cluster_name(),
            True,
        )

    def create_tls_resources(
        self, multi_cluster_cluster_issuer: str, additional_om_domains: Optional[list[str]] = None
    ):
        print("MultiClusterOMClusterWideTestHelper: create_tls_resources")

        self.om_issuer_ca_configmap = create_issuer_ca_configmap(
            get_issuer_ca_filepath(),
            namespace=self.OM_NAMESPACE,
            name="om-issuer-ca",
            api_client=get_central_cluster_client(),
        )

        self.appdb_issuer_ca_configmap = create_issuer_ca_configmap(
            get_issuer_ca_filepath(),
            namespace=self.OM_NAMESPACE,
            name="appdb-issuer-ca",
            api_client=get_central_cluster_client(),
        )

        self.ops_manager_cert_secret_name = create_ops_manager_tls_certs(
            multi_cluster_cluster_issuer,
            self.OM_NAMESPACE,
            self.OM_NAME,
            secret_name=f"{self.OM_CERT_PREFIX}-{self.OM_NAME}-cert",
            clusterwide=True,
            additional_domains=additional_om_domains,
        )

        self.appdb_cert_secret_prefix = create_appdb_certs(
            self.OM_NAMESPACE,
            multi_cluster_cluster_issuer,
            self.OM_NAME + "-db",
            cluster_index_with_members=self.APPDB_CLUSTER_INDEX_WITH_MEMBERS,
            cert_prefix=self.APPDB_CERT_PREFIX,
            clusterwide=True,
        )

        self.s3_bucket_blockstore = next(
            create_s3_bucket_blockstore(self.OM_NAMESPACE, get_aws_s3_client(), api_client=get_central_cluster_client())
        )
        self.s3_bucket_oplog = next(
            create_s3_bucket_oplog(self.OM_NAMESPACE, get_aws_s3_client(), api_client=get_central_cluster_client())
        )

    def ops_manager(self) -> MongoDBOpsManager:
        central_cluster_client = get_central_cluster_client()

        resource = ops_manager_multi_cluster_with_tls_s3_backups(
            self.OM_NAMESPACE,
            self.OM_NAME,
            central_cluster_client,
            get_custom_appdb_version(),
            self.s3_bucket_blockstore,
            self.s3_bucket_oplog,
        )

        if try_load(resource):
            return resource

        resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
        resource["spec"]["version"] = get_custom_om_version()
        resource["spec"]["topology"] = "MultiCluster"
        ## Force creating headless services for internal connectivity
        resource["spec"]["internalConnectivity"] = {
            "type": "ClusterIP",
            "ClusterIP": "None",
        }
        resource["spec"]["clusterSpecList"] = cluster_spec_list(
            [MEMBER_CLUSTER_2], [1], backup_configs=[{"members": 1}]
        )
        resource["spec"]["security"] = {
            "certsSecretPrefix": self.OM_CERT_PREFIX,
            "tls": {
                "ca": self.om_issuer_ca_configmap,
            },
        }

        resource.create_admin_secret(api_client=central_cluster_client)

        resource["spec"]["applicationDatabase"] = {
            "topology": "MultiCluster",
            "clusterSpecList": cluster_spec_list([MEMBER_CLUSTER_3], [3]),
            "version": get_custom_appdb_version(),
            "agent": {"logLevel": "DEBUG"},
            "security": {
                "certsSecretPrefix": self.APPDB_CERT_PREFIX,
                "tls": {
                    "ca": self.appdb_issuer_ca_configmap,
                },
            },
        }

        return resource


@fixture(scope="module")
def om_test_helper(multi_cluster_clusterissuer: str) -> MultiClusterOMClusterWideTestHelper:
    test_helper = MultiClusterOMClusterWideTestHelper()
    test_helper.prepare_namespaces()
    test_helper.create_tls_resources(
        multi_cluster_clusterissuer,
        additional_om_domains=[get_om_external_host(test_helper.OM_NAMESPACE, test_helper.OM_NAME)],
    )

    return test_helper


@mark.e2e_multi_cluster_om_clusterwide_operator_not_in_mesh_networking
def test_deploy_operator(om_test_helper: MultiClusterOMClusterWideTestHelper):
    install_multi_cluster_operator_cluster_scoped(watch_namespaces=[get_namespace(), om_test_helper.OM_NAMESPACE])


@mark.e2e_multi_cluster_om_clusterwide_operator_not_in_mesh_networking
def test_deploy_ops_manager(om_test_helper: MultiClusterOMClusterWideTestHelper):
    ops_manager = om_test_helper.ops_manager()
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Pending, msg_regexp="Enabling monitoring", timeout=900)
    # the operator cannot connect to OM instance so it cannot finish configuring the deployment
    ops_manager.om_status().assert_reaches_phase(
        Phase.Failed,
        msg_regexp="Failed to create an admin user in Ops Manager: Error sending POST request",
        timeout=900,
    )


def get_external_service_ip(ops_manager: MongoDBOpsManager):
    try:
        svc = read_service(
            ops_manager.namespace,
            ops_manager.external_svc_name(MEMBER_CLUSTER_2),
            get_member_cluster_api_client(MEMBER_CLUSTER_2),
        )
    except ApiException as e:
        if e.status == 404:
            return None
        else:
            raise e

    external_ip = None
    if svc.status.load_balancer.ingress:
        external_ip = svc.status.load_balancer.ingress[0].ip

    return external_ip


@mark.e2e_multi_cluster_om_clusterwide_operator_not_in_mesh_networking
def test_enable_external_connectivity(om_test_helper: MultiClusterOMClusterWideTestHelper):
    ops_manager = om_test_helper.ops_manager()
    # TODO make it only to work by overriding it in one member cluster
    ops_manager["spec"]["externalConnectivity"] = {
        "type": "LoadBalancer",
        "port": 9000,
    }
    ops_manager.update()

    def external_ip_available(om: MongoDBOpsManager):
        return get_external_service_ip(om)

    print("Waiting for external service's external IP address")
    ops_manager.wait_for(external_ip_available, 60, should_raise=True)


def get_om_external_host(namespace: str, name: str):
    return f"{name}-svc-ext.{namespace}.interconnected"


@mark.e2e_multi_cluster_om_clusterwide_operator_not_in_mesh_networking
def test_configure_dns(om_test_helper: MultiClusterOMClusterWideTestHelper):
    ops_manager = om_test_helper.ops_manager()
    interconnected_field = get_om_external_host(ops_manager.namespace, ops_manager.name)
    ip = get_external_service_ip(ops_manager)

    # let's make sure that every client can connect to OM.
    for c in get_member_cluster_clients():
        update_coredns_hosts(
            host_mappings=[(ip, interconnected_field)],
            api_client=c.api_client,
            cluster_name=c.cluster_name,
        )

    # let's make sure that the operator can connect to OM via that given address.
    update_coredns_hosts(
        host_mappings=[(ip, interconnected_field)],
        api_client=get_central_cluster_client(),
        cluster_name=get_central_cluster_name(),
    )


@mark.e2e_multi_cluster_om_clusterwide_operator_not_in_mesh_networking
def test_set_ops_manager_url(om_test_helper: MultiClusterOMClusterWideTestHelper):
    ops_manager = om_test_helper.ops_manager()
    host = get_om_external_host(ops_manager.namespace, ops_manager.name)
    ops_manager["spec"]["opsManagerURL"] = f"https://{host}:9000"
    ops_manager.update()

    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
