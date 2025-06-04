import copy
from typing import List, Optional

import kubernetes
from kubeobject import CustomObject
from kubetester.certs import (
    SUBJECT,
    generate_cert,
    get_agent_x509_subject,
    get_mongodb_x509_subject,
    multi_cluster_service_fqdns,
)
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient


def create_multi_cluster_agent_certs(
    multi_cluster_issuer: str,
    secret_name: str,
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_multi: MongoDBMulti,
    secret_backend: Optional[str] = None,
) -> str:
    agents = ["mms-automation-agent"]
    subject = copy.deepcopy(SUBJECT)
    subject["organizationalUnits"] = [mongodb_multi.namespace]

    spec = {
        "subject": subject,
        "usages": ["client auth"],
    }
    spec["dnsNames"] = agents
    spec["commonName"] = "mms-automation-agent"
    return generate_cert(
        namespace=mongodb_multi.namespace,
        pod="tmp",
        dns="",
        issuer=multi_cluster_issuer,
        spec=spec,
        multi_cluster_mode=True,
        api_client=central_cluster_client,
        secret_backend=secret_backend,
        secret_name=secret_name,
        vault_subpath="database",
    )


def create_multi_cluster_x509_agent_certs(
    multi_cluster_issuer: str,
    secret_name: str,
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_multi: MongoDBMulti,
    secret_backend: Optional[str] = None,
) -> str:
    spec = get_agent_x509_subject(mongodb_multi.namespace)

    return generate_cert(
        namespace=mongodb_multi.namespace,
        pod="tmp",
        dns="",
        issuer=multi_cluster_issuer,
        spec=spec,
        multi_cluster_mode=True,
        api_client=central_cluster_client,
        secret_backend=secret_backend,
        secret_name=secret_name,
        vault_subpath="database",
    )


def create_multi_cluster_mongodb_tls_certs(
    multi_cluster_issuer: str,
    bundle_secret_name: str,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_multi: Optional[MongoDBMulti] = None,
    namespace: Optional[str] = None,
    additional_domains: Optional[List[str]] = None,
    service_fqdns: Optional[List[str]] = None,
    clusterwide: bool = False,
) -> str:
    # create the "source-of-truth" tls cert in central cluster
    create_multi_cluster_tls_certs(
        multi_cluster_issuer=multi_cluster_issuer,
        central_cluster_client=central_cluster_client,
        member_clients=member_cluster_clients,
        secret_name=bundle_secret_name,
        mongodb_multi=mongodb_multi,
        namespace=namespace,
        additional_domains=additional_domains,
        service_fqdns=service_fqdns,
        clusterwide=clusterwide,
    )

    return bundle_secret_name


def create_multi_cluster_mongodb_x509_tls_certs(
    multi_cluster_issuer: str,
    bundle_secret_name: str,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_multi: MongoDBMulti,
    additional_domains: Optional[List[str]] = None,
    service_fqdns: Optional[List[str]] = None,
    clusterwide: bool = False,
) -> str:
    spec = get_mongodb_x509_subject(mongodb_multi.namespace)

    # create the "source-of-truth" tls cert in central cluster
    create_multi_cluster_tls_certs(
        multi_cluster_issuer=multi_cluster_issuer,
        central_cluster_client=central_cluster_client,
        member_clients=member_cluster_clients,
        secret_name=bundle_secret_name,
        mongodb_multi=mongodb_multi,
        additional_domains=additional_domains,
        service_fqdns=service_fqdns,
        clusterwide=clusterwide,
        spec=spec,
    )

    return bundle_secret_name


def create_multi_cluster_tls_certs(
    multi_cluster_issuer: str,
    secret_name: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_clients: List[MultiClusterClient],
    mongodb_multi: Optional[CustomObject] = None,
    namespace: Optional[str] = None,
    secret_backend: Optional[str] = None,
    additional_domains: Optional[List[str]] = None,
    service_fqdns: Optional[List[str]] = None,
    clusterwide: bool = False,
    spec: Optional[dict] = None,
) -> str:
    if service_fqdns is None:
        service_fqdns = [f"{mongodb_multi.name}-svc.{mongodb_multi.namespace}.svc.cluster.local"]

        for client in member_clients:
            cluster_spec = mongodb_multi.get_item_spec(client.cluster_name)
            try:
                external_domain = cluster_spec["externalAccess"]["externalDomain"]
            except KeyError:
                external_domain = None
            service_fqdns.extend(
                multi_cluster_service_fqdns(
                    mongodb_multi.name,
                    mongodb_multi.namespace,
                    external_domain,
                    client.cluster_index,
                    cluster_spec["members"],
                )
            )

    if namespace is None:
        namespace = mongodb_multi.namespace

    generate_cert(
        namespace=namespace,
        pod="tmp",
        dns="",
        issuer=multi_cluster_issuer,
        additional_domains=additional_domains,
        multi_cluster_mode=True,
        api_client=central_cluster_client,
        secret_backend=secret_backend,
        secret_name=secret_name,
        vault_subpath="database",
        dns_list=service_fqdns,
        spec=spec,
        clusterwide=clusterwide,
    )

    return secret_name
