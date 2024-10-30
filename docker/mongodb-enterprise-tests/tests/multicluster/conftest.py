#!/usr/bin/env python3
import ipaddress
import urllib
from typing import Dict, Generator, List, Optional
from urllib import parse

import kubernetes
from kubeobject import CustomObject
from kubetester import create_or_update_namespace, create_or_update_secret
from kubetester.certs import generate_cert
from kubetester.kubetester import create_testing_namespace
from kubetester.ldap import (
    LDAPUser,
    OpenLDAP,
    add_user_to_group,
    create_user,
    ensure_group,
    ensure_organizational_unit,
    ldap_initialize,
)
from kubetester.mongodb_multi import MultiClusterClient
from pytest import fixture
from tests.authentication.conftest import (
    AUTOMATION_AGENT_NAME,
    LDAP_NAME,
    LDAP_PASSWORD,
    ldap_host,
    openldap_install,
)
from tests.conftest import (
    create_issuer,
    get_api_servers_from_test_pod_kubeconfig,
    install_cert_manager,
    wait_for_cert_manager_ready,
)


@fixture(scope="module")
def member_cluster_cert_manager(member_cluster_clients: List[MultiClusterClient]) -> str:
    member_cluster_one = member_cluster_clients[0]
    result = install_cert_manager(
        cluster_client=member_cluster_one.api_client,
        cluster_name=member_cluster_one.cluster_name,
    )
    wait_for_cert_manager_ready(cluster_client=member_cluster_one.api_client)
    return result


@fixture(scope="module")
def multi_cluster_ldap_issuer(
    member_cluster_cert_manager: str,
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
def multicluster_openldap_cert(member_cluster_clients: List[MultiClusterClient], multi_cluster_ldap_issuer: str) -> str:
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
def ldap_mongodb_users(multicluster_openldap_tls: OpenLDAP, ca_path: str) -> List[LDAPUser]:
    user_list = [LDAPUser("mdb0", LDAP_PASSWORD)]
    for user in user_list:
        create_user(multicluster_openldap_tls, user, ou="groups", ca_path=ca_path)
    return user_list


@fixture(scope="module")
def ldap_mongodb_agent_user(multicluster_openldap_tls: OpenLDAP, ca_path: str) -> LDAPUser:
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
    return create_service_entries_objects(namespace, central_cluster_client, member_cluster_names)


@fixture(scope="module")
def test_patch_central_namespace(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> str:
    corev1 = kubernetes.client.CoreV1Api(api_client=central_cluster_client)
    ns = corev1.read_namespace(namespace)
    ns.metadata.labels["istio-injection"] = "enabled"
    corev1.patch_namespace(namespace, ns)


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

    api_servers = get_api_servers_from_test_pod_kubeconfig(namespace, member_cluster_names)

    host_parse_results = [urllib.parse.urlparse(api_servers[member_cluster]) for member_cluster in member_cluster_names]

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
    member_configs: Optional[List[List[Dict]]] = None,
    backup_configs: Optional[List[Dict]] = None,
):

    if member_configs is None and backup_configs is None:
        result = []
        for name, members in zip(member_cluster_names, members):
            if members is not None:
                result.append({"clusterName": name, "members": members})
        return result
    elif member_configs is not None:
        result = []
        for name, members, memberConfig in zip(member_cluster_names, members, member_configs):
            if members is not None:
                result.append({"clusterName": name, "members": members, "memberConfig": memberConfig})
        return result
    elif backup_configs is not None:
        result = []
        for name, members, backupConfig in zip(member_cluster_names, members, backup_configs):
            if members is not None:
                result.append({"clusterName": name, "members": members, "backup": backupConfig})
        return result


def create_namespace(
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    task_id: str,
    namespace: str,
    image_pull_secret_name: str,
    image_pull_secret_data: Dict[str, str],
) -> str:
    for member_cluster_client in member_cluster_clients:
        create_testing_namespace(task_id, namespace, member_cluster_client.api_client, True)
        create_or_update_secret(
            namespace,
            image_pull_secret_name,
            image_pull_secret_data,
            type="kubernetes.io/dockerconfigjson",
            api_client=member_cluster_client.api_client,
        )

    create_testing_namespace(task_id, namespace, central_cluster_client)
    create_or_update_secret(
        namespace,
        image_pull_secret_name,
        image_pull_secret_data,
        type="kubernetes.io/dockerconfigjson",
        api_client=central_cluster_client,
    )

    return namespace
