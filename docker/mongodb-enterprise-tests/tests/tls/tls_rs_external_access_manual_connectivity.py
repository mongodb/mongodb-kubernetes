from typing import List

import pytest
import yaml
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator

# This test will set up an environment which will configure a resource with split horizon enabled.

# Steps to run this test.

# 1. Change the following variable to any worker node in your cluster. (describe output of node)
WORKER_NODE_HOSTNAME = "ec2-18-222-120-63.us-east-2.compute.amazonaws.com"

# 2. Run the test `make e2e test=e2e_tls_rs_external_access_manual_connectivity`

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
#   * Run "mongo "mongodb://${WORKER_NODE}:30100,${WORKER_NODE}:30101,${WORKER_NODE}:30102/?replicaSet=test-tls-base-rs-external-access" --tls --tlsCertificateKeyFile server.pem --tlsCAFile ca.crt"
# When split horizon is not configured, specifying the replicaset name should fail.
# When split horizon is configured, it will successfully connect to the primary.

# 6. Clean the namespace
#   * This test creates node ports, which we should delete.


@pytest.fixture(scope="module")
def worker_node_hostname() -> str:
    return WORKER_NODE_HOSTNAME


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str, worker_node_hostname: str):
    mdb_resource = "test-tls-base-rs-external-access"
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        mdb_resource,
        f"{mdb_resource}-cert",
        replicas=3,
        additional_domains=[
            worker_node_hostname,
        ],
    )


@pytest.fixture(scope="module")
def node_ports() -> List[int]:
    return [30100, 30101, 30102]


@pytest.fixture(scope="module")
def mdb(
    namespace: str,
    server_certs: str,
    issuer_ca_configmap: str,
    worker_node_hostname: str,
    node_ports: List[int],
) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("test-tls-base-rs-external-access.yaml"), namespace=namespace)
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    res["spec"]["connectivity"]["replicaSetHorizons"] = [
        {"test-horizon": f"{worker_node_hostname}:{node_ports[0]}"},
        {"test-horizon": f"{worker_node_hostname}:{node_ports[1]}"},
        {"test-horizon": f"{worker_node_hostname}:{node_ports[2]}"},
    ]

    return res.create()


@pytest.mark.e2e_tls_rs_external_access_manual_connectivity
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_tls_rs_external_access_manual_connectivity
def test_mdb_reaches_running_state(mdb: MongoDB):
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_tls_rs_external_access_manual_connectivity
def test_create_node_ports(mdb: MongoDB, node_ports: List[int]):
    for i in range(mdb["spec"]["members"]):
        with open(
            load_fixture("node-port-service.yaml"),
            "r",
        ) as f:
            service_body = yaml.safe_load(f.read())
            service_body["metadata"]["name"] = f"{mdb.name}-external-access-svc-external-{i}"
            service_body["spec"]["ports"][0]["nodePort"] = node_ports[i]
            service_body["spec"]["selector"]["statefulset.kubernetes.io/pod-name"] = f"{mdb.name}-{i}"

            KubernetesTester.create_service(
                mdb.namespace,
                body=service_body,
            )
