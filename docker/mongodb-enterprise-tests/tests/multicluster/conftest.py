#!/usr/bin/env python3
import re
import urllib
from typing import Generator, List, Dict
from urllib import parse

import kubernetes
from kubeobject import CustomObject
from kubernetes import client
from kubernetes.client import V1ObjectMeta
from pytest import fixture

from kubetester import create_or_update_namespace
from kubetester.certs import generate_cert
from kubetester.ldap import (
    OpenLDAP,
    LDAPUser,
    create_user,
    ensure_organizational_unit,
    ensure_group,
    add_user_to_group,
    ldap_initialize,
)
from kubetester.mongodb_multi import MultiClusterClient
from tests.authentication.conftest import (
    openldap_install,
    LDAP_NAME,
    LDAP_PASSWORD,
    AUTOMATION_AGENT_NAME,
    ldap_host,
)
from tests.conftest import (
    get_api_servers_from_test_pod_kubeconfig,
    get_clients_for_clusters,
    create_issuer,
)

import ipaddress


@fixture(scope="module")
def multi_cluster_ldap_issuer(
    cert_manager: str,
    member_cluster_clients: List[MultiClusterClient],
):
    member_cluster_one = member_cluster_clients[0]
    # create openldap namespace if it doesn't exist
    create_or_update_namespace(
        "openldap",
        {"istio-injection": "enabled"},
        api_client=member_cluster_one.api_client,
    )

    return create_issuer("openldap", member_cluster_one.api_client)


@fixture(scope="module")
def multicluster_openldap_cert(
    member_cluster_clients: List[MultiClusterClient], multi_cluster_ldap_issuer: str
) -> str:
    """Returns a new secret to be used to enable TLS on LDAP."""

    member_cluster_one = member_cluster_clients[0]

    host = ldap_host("openldap", LDAP_NAME)
    return generate_cert(
        "openldap",
        "openldap",
        host,
        multi_cluster_ldap_issuer,
        api_client=member_cluster_one.api_client,
    )


@fixture(scope="module")
def multicluster_openldap_tls(
    member_cluster_clients: List[MultiClusterClient],
    multicluster_openldap_cert: str,
    ca_path: str,
) -> Generator[OpenLDAP, None, None]:
    member_cluster_one = member_cluster_clients[0]
    helm_args = {
        "tls.enabled": "true",
        "tls.secret": multicluster_openldap_cert,
        # Do not require client certificates
        "env.LDAP_TLS_VERIFY_CLIENT": "never",
    }
    server = openldap_install(
        "openldap",
        LDAP_NAME,
        helm_args=helm_args,
        cluster_client=member_cluster_one.api_client,
        cluster_name=member_cluster_one.cluster_name,
        tls=True,
    )
    # When creating a new OpenLDAP container with TLS enabled, the container is ready, but the server is not accepting
    # requests, as it's generating DH parameters for the TLS config. Only using retries!=0 for ldap_initialize when creating
    # the OpenLDAP server.
    ldap_initialize(server, ca_path=ca_path, retries=10)
    return server


@fixture(scope="module")
def ldap_mongodb_user(multicluster_openldap_tls: OpenLDAP, ca_path: str) -> LDAPUser:
    user = LDAPUser("mdb0", LDAP_PASSWORD)

    ensure_organizational_unit(multicluster_openldap_tls, "groups", ca_path=ca_path)
    create_user(multicluster_openldap_tls, user, ou="groups", ca_path=ca_path)

    ensure_group(multicluster_openldap_tls, cn="users", ou="groups", ca_path=ca_path)
    add_user_to_group(
        multicluster_openldap_tls,
        user="mdb0",
        group_cn="users",
        ou="groups",
        ca_path=ca_path,
    )

    return user


@fixture(scope="module")
def ldap_mongodb_users(
    multicluster_openldap_tls: OpenLDAP, ca_path: str
) -> List[LDAPUser]:
    user_list = [LDAPUser("mdb0", LDAP_PASSWORD)]
    for user in user_list:
        create_user(multicluster_openldap_tls, user, ou="groups", ca_path=ca_path)
    return user_list


@fixture(scope="module")
def ldap_mongodb_agent_user(
    multicluster_openldap_tls: OpenLDAP, ca_path: str
) -> LDAPUser:
    user = LDAPUser(AUTOMATION_AGENT_NAME, LDAP_PASSWORD)

    ensure_organizational_unit(multicluster_openldap_tls, "groups", ca_path=ca_path)
    create_user(multicluster_openldap_tls, user, ou="groups", ca_path=ca_path)

    ensure_group(multicluster_openldap_tls, cn="agents", ou="groups", ca_path=ca_path)
    add_user_to_group(
        multicluster_openldap_tls,
        user=AUTOMATION_AGENT_NAME,
        group_cn="agents",
        ou="groups",
        ca_path=ca_path,
    )

    return user


# more details https://istio.io/latest/docs/tasks/traffic-management/egress/egress-control/
@fixture(scope="module")
def service_entries(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
) -> List[CustomObject]:
    return create_service_entries_objects(
        namespace, central_cluster_client, member_cluster_names
    )


def check_valid_ip(ip_str: str) -> bool:
    try:
        ipaddress.ip_address(ip_str)
        return True
    except ValueError:
        return False


def create_service_entries_objects(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
) -> List[CustomObject]:
    service_entries = []

    allowed_hosts_service_entry = CustomObject(
        name="allowed-hosts",
        namespace=namespace,
        kind="ServiceEntry",
        plural="serviceentries",
        group="networking.istio.io",
        version="v1beta1",
        api_client=central_cluster_client,
    )

    allowed_addresses_service_entry = CustomObject(
        name="allowed-addresses",
        namespace=namespace,
        kind="ServiceEntry",
        plural="serviceentries",
        group="networking.istio.io",
        version="v1beta1",
        api_client=central_cluster_client,
    )

    api_servers = get_api_servers_from_test_pod_kubeconfig(
        namespace, member_cluster_names
    )

    host_parse_results = [
        urllib.parse.urlparse(api_servers[member_cluster])
        for member_cluster in member_cluster_names
    ]

    hosts = set(["cloud-qa.mongodb.com"])
    addresses = set()
    ports = [{"name": "https", "number": 443, "protocol": "HTTPS"}]

    for host_parse_result in host_parse_results:
        if host_parse_result.port is not None and host_parse_result.port != "":
            ports.append(
                {
                    "name": f"https-{host_parse_result.port}",
                    "number": host_parse_result.port,
                    "protocol": "HTTPS",
                }
            )
        if check_valid_ip(host_parse_result.hostname):
            addresses.add(host_parse_result.hostname)
        else:
            hosts.add(host_parse_result.hostname)

    allowed_hosts_service_entry["spec"] = {
        # by default the access mode is set to "REGISTRY_ONLY" which means only the hosts specified
        # here would be accessible from the operator pod
        "hosts": list(hosts),
        "exportTo": ["."],
        "location": "MESH_EXTERNAL",
        "ports": ports,
        "resolution": "DNS",
    }
    service_entries.append(allowed_hosts_service_entry)

    if len(addresses) > 0:
        allowed_addresses_service_entry["spec"] = {
            "hosts": ["kubernetes", "kubernetes-master", "kube-apiserver"],
            "addresses": list(addresses),
            "exportTo": ["."],
            "location": "MESH_EXTERNAL",
            "ports": ports,
            # when allowing by IP address we do not want to resolve IP using HTTP host field
            "resolution": "NONE",
        }
        service_entries.append(allowed_addresses_service_entry)

    return service_entries


def cluster_spec_list(
    member_cluster_names: List[str],
    members: List[int],
    member_configs: List[Dict] = None,
):
    if member_configs is None:
        return [
            {"clusterName": name, "members": members}
            for (name, members) in zip(member_cluster_names, members)
        ]
    else:
        return [
            {"clusterName": name, "members": members, "memberConfig": memberConfig}
            for (name, members, memberConfig) in zip(
                member_cluster_names, members, member_configs
            )
        ]
