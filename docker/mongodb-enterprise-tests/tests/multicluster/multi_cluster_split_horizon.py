from typing import List

import kubernetes
import yaml
from kubetester.certs import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.mongotester import with_tls
from kubetester.operator import Operator
from pytest import fixture, mark

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"
USER_NAME = "my-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"

# This test will set up an environment which will configure a resource with split horizon enabled.
# Steps to run this test.

# 1. Change the nodenames under "additional_domains"
# 2. Run this test with: `make e2e test=e2e_multi_cluster_split_horizon light=true local=true`.
# 3. Wait for the test to pass (this means the environment is set up.)
# 4. Exec into any database pod and note the contents of the files referenced by the fields
#  * net.tls.certificateKeyFile
#  * net.tlsCAFile
# from the /data/automation-mongod.conf file.

# 5. Test the connection
# Testing the connection can be done from either the worker node or from your local machine(note accessing traffic from a pod inside the cluster would work irrespective SH is configured correctly or not)
# 1. Acsessing from worker node
#   * ssh into any worker node
#   * Install the mongo shell
#   * Create files from the two files mentioned above. (server.pem and ca.crt)
#   * Run "mongo "mongodb://${WORKER_NODE}:30100,${WORKER_NODE}:30101,${WORKER_NODE}:30102/?replicaSet=test-tls-base-rs-external-access" --tls --tlsCertificateKeyFile server.pem --tlsCAFile ca.crt"
# 2. Accessing from local machine
#   * Install the mongo shell
#   * Create files from the two files mentioned above. (server.pem and ca.crt)
#   * Open access to KOPS nodes from your local machine by following these steps(by default KOPS doesn't expose traffic from all ports to the internet)
#     : https://stackoverflow.com/questions/45543694/kubernetes-cluster-on-aws-with-kops-nodeport-service-unavailable/45561848#45561848
#   * Run "mongo "mongodb://${WORKER_NODE1}:30100,${WORKER_NODE2}:30101,${WORKER_NODE3}:30102/?replicaSet=test-tls-base-rs-external-access" --tls --tlsCertificateKeyFile server.pem --tlsCAFile ca.crt"
# When split horizon is not configured, specifying the replicaset name should fail.
# When split horizon is configured, it will successfully connect to the primary.

# Example: mongo "mongodb://ec2-35-178-71-70.eu-west-2.compute.amazonaws.com:30100,ec2-52-56-69-123.eu-west-2.compute.amazonaws.com:30100,ec2-3-10-22-163.eu-west-2.compute.amazonaws.com:30100" --tls --tlsCertificateKeyFile server.pem --tlsCAFile ca-pem

# 6. Clean the namespace
#   * This test creates node ports, which we should delete.


@fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi-split-horizon.yaml"), MDB_RESOURCE, namespace)
    return resource


@fixture(scope="module")
def server_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):

    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi_unmarshalled,
        additional_domains=[
            "*",
            "ec2-35-178-71-70.eu-west-2.compute.amazonaws.com",
            "ec2-52-56-69-123.eu-west-2.compute.amazonaws.com",
            "ec2-3-10-22-163.eu-west-2.compute.amazonaws.com",
        ],
    )


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    server_certs: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    multi_cluster_issuer_ca_configmap: str,
) -> MongoDBMulti:

    resource = mongodb_multi_unmarshalled
    resource["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {
            "enabled": True,
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.create()


@mark.e2e_multi_cluster_split_horizon
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_split_horizon
def test_deploy_mongodb_multi_with_tls(
    mongodb_multi: MongoDBMulti,
    namespace: str,
):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_multi_cluster_split_horizon
def test_create_node_ports(mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]):
    for mcc in member_cluster_clients:
        with open(
            yaml_fixture(f"split-horizon-node-ports/split-horizon-node-port.yaml"),
            "r",
        ) as f:
            service_body = yaml.safe_load(f.read())

            # configure labels and selectors
            service_body["metadata"]["labels"][
                "mongodbmulticluster"
            ] = f"{mongodb_multi.namespace}-{mongodb_multi.name}"
            service_body["metadata"]["labels"][
                "statefulset.kubernetes.io/pod-name"
            ] = f"{mongodb_multi.name}-{mcc.cluster_index}-0"
            service_body["spec"]["selector"][
                "statefulset.kubernetes.io/pod-name"
            ] = f"{mongodb_multi.name}-{mcc.cluster_index}-0"

            KubernetesTester.create_service(
                mongodb_multi.namespace,
                body=service_body,
                api_client=mcc.api_client,
            )


@skip_if_local
@mark.e2e_multi_cluster_split_horizon
def test_tls_connectivity(mongodb_multi: MongoDBMulti, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])
