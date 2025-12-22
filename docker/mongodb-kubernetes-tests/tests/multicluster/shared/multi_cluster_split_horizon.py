from typing import List

import yaml
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongotester import with_tls
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_deploy_mongodb_multi_with_tls(
    mongodb_multi: MongoDBMulti | MongoDB,
    namespace: str,
):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


def test_create_node_ports(mongodb_multi: MongoDBMulti | MongoDB, member_cluster_clients: List[MultiClusterClient]):
    for mcc in member_cluster_clients:
        with open(
            yaml_fixture(f"split-horizon-node-ports/mongodbmulticluster-split-horizon-node-port.yaml"),
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


def test_tls_connectivity(mongodb_multi: MongoDBMulti | MongoDB, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])
