"""
Certificate Custom Resource Definition.
"""

import collections

from kubetester.mongodb import MongoDB
from datetime import datetime, timezone
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester.kubetester import KubernetesTester
from kubetester import random_k8s_name, create_secret, read_secret, delete_secret
from typing import Optional, Dict, List, Generator
from kubeobject import CustomObject
import copy
import time
import random
from pytest import fixture, mark
import os
import tempfile

import kubernetes
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from tests.vaultintegration import (
    store_secret_in_vault,
    vault_namespace_name,
    vault_sts_name,
)

ISSUER_CA_NAME = "ca-issuer"

SUBJECT = {
    # Organizational Units matches your namespace (to be overriden by test)
    "organizationalUnits": ["TO-BE-REPLACED"],
}

# Defines properties of a set of servers, like a Shard, or Replica Set holding config servers.
# This is almost equivalent to the StatefulSet created.
SetProperties = collections.namedtuple("SetProperties", ["name", "service", "replicas"])


CertificateType = CustomObject.define(
    "Certificate",
    kind="Certificate",
    plural="certificates",
    group="cert-manager.io",
    version="v1",
)


class WaitForConditions:
    def is_ready(self):
        self.reload()

        if "status" not in self or "conditions" not in self["status"]:
            return

        for condition in self["status"]["conditions"]:
            if (
                condition["reason"] == self.Reason
                and condition["status"] == "True"
                and condition["type"] == "Ready"
            ):
                return True

    def block_until_ready(self):
        while not self.is_ready():
            time.sleep(2)


class Certificate(CertificateType, WaitForConditions):
    Reason = "Ready"


IssuerType = CustomObject.define(
    "Issuer", kind="Issuer", plural="issuers", group="cert-manager.io", version="v1"
)


class Issuer(IssuerType, WaitForConditions):
    Reason = "KeyPairVerified"


def generate_cert(
    namespace: str,
    pod: str,
    dns: str,
    issuer: str,
    spec: Optional[Dict] = None,
    additional_domains: Optional[List[str]] = None,
    multi_cluster_mode=False,
    api_client: Optional[client.ApiClient] = None,
    secret_name: Optional[str] = None,
    secret_backend: Optional[str] = None,
    vault_subpath: Optional[str] = None,
    dns_list: Optional[List[str]] = None,
) -> str:
    if spec is None:
        spec = dict()

    if secret_name is None:
        secret_name = "{}-{}".format(pod[0], random_k8s_name(prefix="")[:4])

    if secret_backend is None:
        secret_backend = "Kubernetes"

    cert = Certificate(namespace=namespace, name=secret_name)

    if multi_cluster_mode:
        dns_names = dns_list
    else:
        dns_names = [dns]

    if not multi_cluster_mode:
        dns_names.append(pod)

    if additional_domains is not None:
        dns_names += additional_domains

    cert["spec"] = {
        "dnsNames": dns_names,
        "secretName": secret_name,
        "issuerRef": {"name": issuer},
        "duration": "240h",
        "renewBefore": "120h",
        "usages": ["server auth", "client auth"],
    }
    cert["spec"].update(spec)
    cert.api = kubernetes.client.CustomObjectsApi(api_client=api_client)
    cert.create().block_until_ready()

    if secret_backend == "Vault":
        path = "secret/mongodbenterprise/"
        if vault_subpath is None:
            raise ValueError(
                "When secret backend is Vault, a subpath must be specified"
            )
        path += f"{vault_subpath}/{namespace}/{secret_name}"

        data = read_secret(namespace, secret_name)
        store_secret_in_vault(vault_namespace_name(), vault_sts_name(), data, path)
        cert.delete()
        delete_secret(namespace, secret_name)

    return secret_name


def create_tls_certs(
    issuer: str,
    namespace: str,
    resource_name: str,
    replicas: int = 3,
    service_name: str = None,
    spec: Optional[Dict] = None,
    secret_name: Optional[str] = None,
    additional_domains: Optional[List[str]] = None,
    secret_backend: Optional[str] = None,
    vault_subpath: Optional[str] = None,
) -> Dict[str, str]:
    if service_name is None:
        service_name = resource_name + "-svc"

    if spec is None:
        spec = dict()

    pod_fqdn_fstring = (
        "{resource_name}-{index}.{service_name}.{namespace}.svc.cluster.local".format(
            resource_name=resource_name,
            service_name=service_name,
            namespace=namespace,
            index="{}",
        )
    )

    pod_dns = []
    pods = []
    for idx in range(replicas):
        pod_dns.append(pod_fqdn_fstring.format(idx))
        pods.append(f"{resource_name}-{idx}")

    spec["dnsNames"] = pods + pod_dns
    if additional_domains is not None:
        spec["dnsNames"] += additional_domains

    cert_secret_name = generate_cert(
        namespace=namespace,
        pod=pods,
        dns=pod_dns,
        issuer=issuer,
        spec=spec,
        secret_name=secret_name,
        secret_backend=secret_backend,
        vault_subpath=vault_subpath,
    )
    return cert_secret_name


def create_ops_manager_tls_certs(
    issuer: str,
    namespace: str,
    om_name: str,
    secret_name: Optional[str] = None,
    secret_backend: Optional[str] = None,
    additional_domains: Optional[List[str]] = None,
) -> str:

    certs_secret_name = "certs-for-ops-manager"

    if secret_name is not None:
        certs_secret_name = secret_name

    domain = f"{om_name}-svc.{namespace}.svc.cluster.local"
    hostnames = [domain]
    if additional_domains:
        hostnames += additional_domains

    spec = {"dnsNames": hostnames}

    return generate_cert(
        namespace=namespace,
        pod="foo",
        dns="",
        issuer=issuer,
        spec=spec,
        secret_name=certs_secret_name,
        secret_backend=secret_backend,
        vault_subpath="opsmanager",
    )


def create_vault_certs(
    namespace: str, issuer: str, vault_namespace: str, vault_name: str, secret_name: str
):

    cert = Certificate(namespace=namespace, name=secret_name)

    cert["spec"] = {
        "commonName": f"{vault_name}",
        "ipAddresses": [
            "127.0.0.1",
        ],
        "dnsNames": [
            f"{vault_name}",
            f"{vault_name}.{vault_namespace}",
            f"{vault_name}.{vault_namespace}.svc",
            f"{vault_name}.{vault_namespace}.svc.cluster.local",
        ],
        "secretName": secret_name,
        "issuerRef": {"name": issuer},
        "duration": "240h",
        "renewBefore": "120h",
        "usages": ["server auth", "digital signature", "key encipherment"],
    }

    cert.create().block_until_ready()
    data = read_secret(namespace, secret_name)

    # When re-running locally, we need to delete the secrets, if it exists
    try:
        delete_secret(vault_namespace, secret_name)
    except ApiException:
        pass
    create_secret(vault_namespace, secret_name, data)
    return secret_name


def create_mongodb_tls_certs(
    issuer: str,
    namespace: str,
    resource_name: str,
    bundle_secret_name: str,
    replicas: int = 3,
    service_name: str = None,
    spec: Optional[Dict] = None,
    additional_domains: Optional[List[str]] = None,
    secret_backend: Optional[str] = None,
    vault_subpath: Optional[str] = None,
) -> str:

    cert_and_pod_names = create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=resource_name,
        replicas=replicas,
        service_name=service_name,
        spec=spec,
        additional_domains=additional_domains,
        secret_name=bundle_secret_name,
        secret_backend=secret_backend,
        vault_subpath=vault_subpath,
    )

    return cert_and_pod_names


def multi_cluster_pod_fqdns(
    resource_name: str, namespace: str, cluster_index: str, replicas: int
) -> Dict[str, str]:
    pod_fqdns = []

    for n in range(replicas):
        pod_fqdns.append(
            f"{resource_name}-{cluster_index}-{n}.{resource_name}-{cluster_index}-svc.{namespace}.svc.cluster.local"
        )

    return pod_fqdns


def create_multi_cluster_tls_certs(
    multi_cluster_issuer: str,
    secret_name: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_clients: List[MultiClusterClient],
    mongodb_multi: MongoDBMulti,
    secret_backend: Optional[str] = None,
) -> Dict[str, str]:

    pod_fqdns = []

    for client in member_clients:
        cluster_spec = mongodb_multi.get_item_spec(client.cluster_name)
        pod_fqdns.extend(
            multi_cluster_pod_fqdns(
                mongodb_multi.name,
                mongodb_multi.namespace,
                client.cluster_index,
                cluster_spec["members"],
            )
        )

    cert_secret_name = generate_cert(
        namespace=mongodb_multi.namespace,
        pod="tmp",
        dns="",
        issuer=multi_cluster_issuer,
        multi_cluster_mode=True,
        api_client=central_cluster_client,
        secret_backend=secret_backend,
        secret_name=secret_name,
        vault_subpath="database",
        dns_list=pod_fqdns,
    )

    return secret_name


def create_multi_cluster_mongodb_tls_certs(
    multi_cluster_issuer: str,
    bundle_secret_name: str,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_multi: MongoDBMulti,
) -> str:

    # create the "source-of-truth" tls cert in central cluster
    create_multi_cluster_tls_certs(
        multi_cluster_issuer=multi_cluster_issuer,
        central_cluster_client=central_cluster_client,
        member_clients=member_cluster_clients,
        secret_name=bundle_secret_name,
        mongodb_multi=mongodb_multi,
    )

    return bundle_secret_name


def create_x509_mongodb_tls_certs(
    issuer: str,
    namespace: str,
    resource_name: str,
    bundle_secret_name: str,
    replicas: int = 3,
    service_name: str = None,
    additional_domains: Optional[List[str]] = None,
    secret_backend: Optional[str] = None,
    vault_subpath: Optional[str] = None,
) -> str:

    subject = {}
    subject["countries"] = ["US"]
    subject["provinces"] = ["NY"]
    subject["localities"] = ["NY"]
    subject["organizations"] = ["cluster.local-server"]
    subject["organizationalUnits"] = [namespace]

    spec = {
        "subject": subject,
        "usages": [
            "digital signature",
            "key encipherment",
            "client auth",
            "server auth",
        ],
    }

    return create_mongodb_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=resource_name,
        bundle_secret_name=bundle_secret_name,
        replicas=replicas,
        service_name=service_name,
        spec=spec,
        additional_domains=additional_domains,
        secret_backend=secret_backend,
        vault_subpath=vault_subpath,
    )


def create_agent_tls_certs(
    issuer: str, namespace: str, name: str, secret_backend: Optional[str] = None
) -> str:
    agents = ["mms-automation-agent"]
    subject = copy.deepcopy(SUBJECT)
    subject["organizationalUnits"] = [namespace]

    spec = {
        "subject": subject,
        "usages": ["client auth"],
    }
    spec["dnsNames"] = agents
    spec["commonName"] = "mms-automation-agent"
    secret = generate_cert(
        namespace=namespace,
        pod=[],
        dns=[],
        issuer=issuer,
        spec=spec,
        secret_name="agent-certs",
        secret_backend=secret_backend,
        vault_subpath="database",
    )


def create_sharded_cluster_certs(
    namespace: str,
    resource_name: str,
    shards: int,
    mongos_per_shard: int,
    config_servers: int,
    mongos: int,
    internal_auth: bool = False,
    x509_certs: bool = False,
    additional_domains: Optional[List[str]] = None,
    secret_prefix: Optional[str] = None,
    secret_backend: Optional[str] = None,
):
    cert_generation_func = create_mongodb_tls_certs
    if x509_certs:
        cert_generation_func = create_x509_mongodb_tls_certs

    secret_type = "kubernetes.io/tls"
    for i in range(shards):
        additional_domains_for_shard = None
        if additional_domains is not None:
            additional_domains_for_shard = []
            for domain in additional_domains:
                for j in range(mongos_per_shard):
                    additional_domains_for_shard.append(
                        f"{resource_name}-{i}-{j}.{domain}"
                    )

        secret_name = f"{resource_name}-{i}-cert"
        if secret_prefix is not None:
            secret_name = secret_prefix + secret_name
        cert_generation_func(
            issuer=ISSUER_CA_NAME,
            namespace=namespace,
            resource_name=f"{resource_name}-{i}",
            bundle_secret_name=secret_name,
            replicas=mongos_per_shard,
            service_name=resource_name + "-sh",
            additional_domains=additional_domains_for_shard,
            secret_backend=secret_backend,
        )
        if internal_auth:
            data = read_secret(namespace, f"{resource_name}-{i}-cert")
            create_secret(
                namespace, f"{resource_name}-{i}-clusterfile", data, type=secret_type
            )

    additional_domains_for_config = None
    if additional_domains is not None:
        additional_domains_for_config = []
        for domain in additional_domains:
            for j in range(config_servers):
                additional_domains_for_config.append(
                    f"{resource_name}-config-{j}.{domain}"
                )

    secret_name = f"{resource_name}-config-cert"
    if secret_prefix is not None:
        secret_name = secret_prefix + secret_name
    cert_generation_func(
        issuer=ISSUER_CA_NAME,
        namespace=namespace,
        resource_name=resource_name + "-config",
        bundle_secret_name=secret_name,
        replicas=config_servers,
        service_name=resource_name + "-cs",
        additional_domains=additional_domains_for_config,
        secret_backend=secret_backend,
    )
    if internal_auth:
        data = read_secret(namespace, f"{resource_name}-config-cert")
        create_secret(
            namespace, f"{resource_name}-config-clusterfile", data, type=secret_type
        )

    additional_domains_for_mongos = None
    if additional_domains is not None:
        additional_domains_for_mongos = []
        for domain in additional_domains:
            for j in range(mongos):
                additional_domains_for_mongos.append(
                    f"{resource_name}-mongos-{j}.{domain}"
                )

    secret_name = f"{resource_name}-mongos-cert"
    if secret_prefix is not None:
        secret_name = secret_prefix + secret_name
    cert_generation_func(
        issuer=ISSUER_CA_NAME,
        namespace=namespace,
        resource_name=resource_name + "-mongos",
        bundle_secret_name=secret_name,
        service_name=resource_name + "-svc",
        replicas=mongos,
        additional_domains=additional_domains_for_mongos,
        secret_backend=secret_backend,
    )

    if internal_auth:
        data = read_secret(namespace, f"{resource_name}-mongos-cert")
        create_secret(
            namespace, f"{resource_name}-mongos-clusterfile", data, type=secret_type
        )


def create_x509_agent_tls_certs(
    issuer: str, namespace: str, name: str, secret_backend: Optional[str] = None
) -> str:
    agents = ["automation", "monitoring", "backup"]
    subject = {}
    subject["countries"] = ["US"]
    subject["provinces"] = ["NY"]
    subject["localities"] = ["NY"]
    subject["organizations"] = ["cluster.local-agent"]
    subject["organizationalUnits"] = [namespace]

    spec = {
        "subject": subject,
        "usages": ["digital signature", "key encipherment", "client auth"],
    }

    spec["dnsNames"] = agents
    spec["commonName"] = "mms-automation-agent"
    secret = generate_cert(
        namespace=namespace,
        pod=[],
        dns=[],
        issuer=issuer,
        spec=spec,
        secret_name="agent-certs",
        secret_backend=secret_backend,
        vault_subpath="database",
    )


def approve_certificate(name: str) -> None:
    """Approves the CertificateSigningRequest with the provided name"""
    body = client.CertificatesV1beta1Api().read_certificate_signing_request_status(name)
    conditions = client.V1beta1CertificateSigningRequestCondition(
        last_update_time=datetime.now(timezone.utc).astimezone(),
        message="This certificate was approved by E2E testing framework",
        reason="E2ETestingFramework",
        type="Approved",
    )

    body.status.conditions = [conditions]
    client.CertificatesV1beta1Api().replace_certificate_signing_request_approval(
        name, body
    )


def create_x509_user_cert(issuer: str, namespace: str, path: str):
    user_name = "x509-testing-user"

    spec = {
        "usages": ["digital signature", "key encipherment", "client auth"],
        "commonName": user_name,
    }
    secret = generate_cert(namespace, user_name, user_name, issuer, spec, "mongodbuser")
    cert = KubernetesTester.read_secret(namespace, secret)
    with open(path, mode="w") as f:
        f.write(cert["tls.key"])
        f.write(cert["tls.crt"])
        f.flush()


def yield_existing_csrs(
    csr_names: List[str], timeout: int = 300
) -> Generator[str, None, None]:
    """Returns certificate names as they start appearing in the Kubernetes API."""
    csr_names = csr_names.copy()
    total_csrs = len(csr_names)
    seen_csrs = 0
    stop_time = time.time() + timeout

    while len(csr_names) > 0 and time.time() < stop_time:
        csr = random.choice(csr_names)
        try:
            client.CertificatesV1beta1Api().read_certificate_signing_request_status(csr)
        except ApiException:
            time.sleep(3)
            continue

        seen_csrs += 1
        csr_names.remove(csr)
        yield csr

    if len(csr_names) == 0:
        # All the certificates have been "consumed" and yielded back to the user.
        return

    # we didn't find all of the expected csrs after the timeout period
    raise AssertionError(
        f"Expected to find {total_csrs} csrs, but only found {seen_csrs} after {timeout} seconds. Expected csrs {csr_names}"
    )
