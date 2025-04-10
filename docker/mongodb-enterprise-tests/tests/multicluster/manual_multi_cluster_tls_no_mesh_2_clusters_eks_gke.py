# This is a manual test deploying MongoDBMultiCluster on 2 clusters in GKE and EKS.
# Steps to execute:
# 1. After the test is executed it will create external services of type LoadBalancer. All pods will be not ready
# due to lack of external connectivity.
# 2. Go to mc.mongokubernetes.com Route53 hosted zone: https://us-east-1.console.aws.amazon.com/route53/v2/hostedzones#ListRecordSets/Z04069951X9SBFR8OQUFM
# 3. Copy provisioned hostnames from external services in EKS and update CNAME records for:
#  * multi-cluster-rs-0-0.aws-member-cluster.eks.mc.mongokubernetes.com
#  * multi-cluster-rs-0-1.aws-member-cluster.eks.mc.mongokubernetes.com
# 4. Copy IP addresses of external services in GKE and update A record for:
#  * multi-cluster-rs-1-0.gke-member-cluster.gke.mc.mongokubernetes.com
#  * multi-cluster-rs-1-1.gke-member-cluster.gke.mc.mongokubernetes.com
# 5. After few minutes everything should be ready.

from typing import List

import kubernetes
from kubetester.certs import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture
from tests.multicluster.conftest import cluster_spec_list

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-rs"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"
BUNDLE_PEM_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert-pem"


@fixture(scope="module")
def cert_additional_domains() -> list[str]:
    return [
        "*.gke-member-cluster.gke.mc.mongokubernetes.com",
        "*.aws-member-cluster.eks.mc.mongokubernetes.com",
    ]


@fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str, member_cluster_names: List[str]) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource["spec"]["persistent"] = False
    # These domains map 1:1 to the CoreDNS file. Please be mindful when updating them.
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1])

    resource["spec"]["externalAccess"] = {}
    resource["spec"]["clusterSpecList"][0]["externalAccess"] = {
        "externalDomain": "aws-member-cluster.eks.mc.mongokubernetes.com",
        "externalService": {
            "annotations": {"cloud.google.com/l4-rbs": "enabled"},
            "spec": {
                "type": "LoadBalancer",
                "publishNotReadyAddresses": True,
                "ports": [
                    {
                        "name": "mongodb",
                        "port": 27017,
                    },
                    {
                        "name": "backup",
                        "port": 27018,
                    },
                ],
            },
        },
    }
    resource["spec"]["clusterSpecList"][1]["externalAccess"] = {
        "externalDomain": "gke-member-cluster.gke.mc.mongokubernetes.com",
        "externalService": {
            "annotations": {
                "service.beta.kubernetes.io/aws-load-balancer-type": "external",
                "service.beta.kubernetes.io/aws-load-balancer-nlb-target-type": "instance",
                "service.beta.kubernetes.io/aws-load-balancer-scheme": "internet-facing",
            },
            "spec": {
                "type": "LoadBalancer",
                "publishNotReadyAddresses": True,
                "ports": [
                    {
                        "name": "mongodb",
                        "port": 27017,
                    },
                    {
                        "name": "backup",
                        "port": 27018,
                    },
                ],
            },
        },
    }

    return resource


@fixture(scope="function")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    multi_cluster_issuer_ca_configmap: str,
) -> MongoDBMulti:
    mongodb_multi_unmarshalled["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    mongodb_multi_unmarshalled.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return mongodb_multi_unmarshalled.update()


@fixture(scope="module")
def server_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: list[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
    cert_additional_domains: list[str],
):
    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi_unmarshalled,
        additional_domains=cert_additional_domains,
    )


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    server_certs: str,
    multi_cluster_issuer_ca_configmap: str,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=2400)
