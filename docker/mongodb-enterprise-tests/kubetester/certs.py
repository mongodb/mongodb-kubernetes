"""
Certificate Custom Resource Definition.
"""

import collections

from kubetester.mongodb import MongoDB
from datetime import datetime, timezone
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester.kubetester import KubernetesTester
from kubetester import random_k8s_name, create_secret, read_secret
from typing import Optional, Dict, List, Generator
from kubeobject import CustomObject
import copy
import time
import random
import tempfile

import kubernetes
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient

ISSUER_CA_NAME = "ca-issuer"

SUBJECT = {
    # Organizational Units matches your namespace (to be overriden by test)
    "organizationalUnits": ["TO-BE-REPLACED"],
    # For an additional layer of security, the certificates will have a random
    # (unknown and "unpredictable"), random string. Even if someone was able to
    # generate the certificates themselves, they would still require this
    # value to do so.
}

# Defines properties of a set of servers, like a Shard, or Replica Set holding config servers.
# This is almost equivalent to the StatefulSet created.
SetProperties = collections.namedtuple("SetProperties", ["name", "service", "replicas"])


CertificateType = CustomObject.define(
    "Certificate",
    kind="Certificate",
    plural="certificates",
    group="cert-manager.io",
    version="v1alpha3",
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
    "Issuer", plural="issuers", group="cert-manager.io", version="v1alpha2"
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
) -> str:
    if spec is None:
        spec = dict()
    secret_name = "{}-{}".format(pod, random_k8s_name(prefix="")[:4])
    cert = Certificate(namespace=namespace, name=secret_name)

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

    # Make sure the Secret names used have a random part
    return secret_name


def create_tls_certs(
    issuer: str,
    namespace: str,
    resource_name: str,
    replicas: int = 3,
    service_name: str = None,
    spec: Optional[Dict] = None,
    additional_domains: Optional[List[str]] = None,
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
    secret_and_pod_names = {}
    for idx in range(replicas):
        pod_dns = pod_fqdn_fstring.format(idx)
        pod_name = f"{resource_name}-{idx}"
        cert_secret_name = generate_cert(
            namespace, pod_name, pod_dns, issuer, spec, additional_domains
        )
        secret_and_pod_names[pod_name] = cert_secret_name
    return secret_and_pod_names


def create_ops_manager_tls_certs(
    issuer: str, namespace: str, om_name: str, secret_name: Optional[str] = None
) -> str:

    domain = f"{om_name}-svc.{namespace}.svc.cluster.local"
    spec = {"dnsNames": [domain]}

    secret_name_for_cert = generate_cert(namespace, "foo", "", issuer, spec)
    https_cert = KubernetesTester.read_secret(namespace, secret_name_for_cert)
    data = {"server.pem": https_cert["tls.key"] + https_cert["tls.crt"]}

    certs_secret_name = "certs-for-ops-manager"

    if secret_name is not None:
        certs_secret_name = secret_name

    # Cert and Key file need to be merged into its own PEM file.
    KubernetesTester.create_secret(namespace, certs_secret_name, data)
    return certs_secret_name


def create_mongodb_tls_certs(
    issuer: str,
    namespace: str,
    resource_name: str,
    bundle_secret_name: str,
    replicas: int = 3,
    service_name: str = None,
    spec: Optional[Dict] = None,
    additional_domains: Optional[List[str]] = None,
) -> str:
    cert_and_pod_names = create_tls_certs(
        issuer,
        namespace,
        resource_name,
        replicas,
        service_name,
        spec,
        additional_domains,
    )
    data = {}
    for pod_name, cert_secret_name in cert_and_pod_names.items():
        secret = read_secret(namespace, cert_secret_name)
        data[pod_name + "-pem"] = secret["tls.key"] + secret["tls.crt"]

    create_secret(namespace, bundle_secret_name, data)
    return bundle_secret_name


def mutli_cluster_pod_name_to_service_fqdn_mapping(
    resource_name: str, namespace: str, cluster_index: str, replicas: int
) -> Dict[str, str]:
    pod_to_service_fqdn_mapping = {}

    for n in range(replicas):
        pod_to_service_fqdn_mapping[
            f"{resource_name}-{cluster_index}-{n}"
        ] = f"{resource_name}-{cluster_index}-{n}-svc.{namespace}.svc.cluster.local"

    return pod_to_service_fqdn_mapping


def create_multi_cluster_tls_certs(
    multi_cluster_issuer: str,
    client: MultiClusterClient,
    mongodb_multi: MongoDBMulti,
) -> Dict[str, str]:
    """
    Returns a dictionary of pod_name -> secret_name
    """

    pod_to_secret_name = {}
    # get the service names for a given cluster
    cluster_spec = mongodb_multi.get_item_spec(client.cluster_name)
    pod_to_service_fqdn_mapping = mutli_cluster_pod_name_to_service_fqdn_mapping(
        mongodb_multi.name,
        mongodb_multi.namespace,
        client.cluster_index,
        cluster_spec["members"],
    )
    for pod_name, service_fqdn in pod_to_service_fqdn_mapping.items():
        cert_secret_name = generate_cert(
            mongodb_multi.namespace,
            pod_name,
            service_fqdn,
            multi_cluster_issuer,
            multi_cluster_mode=True,
            api_client=client.api_client,
        )
        pod_to_secret_name[pod_name] = cert_secret_name

    return pod_to_secret_name


def create_multi_cluster_mongodb_tls_certs(
    multi_cluster_isser: str,
    bundle_secret_name: str,
    member_cluster_clients: List[MultiClusterClient],
    mongodb_multi: MongoDBMulti,
) -> str:

    data = {}

    for client in member_cluster_clients:
        pod_to_secret_name = create_multi_cluster_tls_certs(
            multi_cluster_isser, client, mongodb_multi
        )
        for pod_name, cert_secret_name in pod_to_secret_name.items():
            secret = read_secret(
                mongodb_multi.namespace, cert_secret_name, client.api_client
            )
            data[pod_name + "-pem"] = secret["tls.key"] + secret["tls.crt"]

    for client in member_cluster_clients:
        create_secret(
            namespace=mongodb_multi.namespace,
            name=bundle_secret_name,
            data=data,
            api_client=client.api_client,
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

    cert_and_pod_names = create_tls_certs(
        issuer,
        namespace,
        resource_name,
        replicas,
        service_name,
        spec,
        additional_domains,
    )
    data = {}
    for pod_name, cert_secret_name in cert_and_pod_names.items():
        secret = read_secret(namespace, cert_secret_name)
        data[pod_name + "-pem"] = secret["tls.crt"] + secret["tls.key"]

    create_secret(namespace, bundle_secret_name, data)
    return bundle_secret_name


def create_agent_tls_certs(issuer: str, namespace: str, name: str) -> str:
    agents = ["automation", "monitoring", "backup"]
    subject = copy.deepcopy(SUBJECT)
    subject["organizationalUnits"] = [namespace]

    spec = {
        "subject": subject,
        "usages": ["client auth"],
    }

    full_certs = {}
    for agent in agents:
        spec["dnsNames"] = [agent]
        spec["commonName"] = agent
        secret = generate_cert(namespace, agent, agent, issuer, spec)
        agent_cert = KubernetesTester.read_secret(namespace, secret)
        full_certs["mms-{}-agent-pem".format(agent)] = (
            agent_cert["tls.crt"] + agent_cert["tls.key"]
        )
    KubernetesTester.create_secret(namespace, "agent-certs", full_certs)


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
):
    cert_generation_func = create_mongodb_tls_certs
    if x509_certs:
        cert_generation_func = create_x509_mongodb_tls_certs

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
            ISSUER_CA_NAME,
            namespace,
            f"{resource_name}-{i}",
            secret_name,
            replicas=mongos_per_shard,
            service_name=resource_name + "-sh",
            additional_domains=additional_domains_for_shard,
        )
        if internal_auth:
            data = read_secret(namespace, f"{resource_name}-{i}-cert")
            create_secret(namespace, f"{resource_name}-{i}-clusterfile", data)

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
        ISSUER_CA_NAME,
        namespace,
        resource_name + "-config",
        secret_name,
        replicas=config_servers,
        service_name=resource_name + "-cs",
        additional_domains=additional_domains_for_config,
    )
    if internal_auth:
        data = read_secret(namespace, f"{resource_name}-config-cert")
        create_secret(namespace, f"{resource_name}-config-clusterfile", data)

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
        ISSUER_CA_NAME,
        namespace,
        resource_name + "-mongos",
        secret_name,
        service_name=resource_name + "-svc",
        replicas=mongos,
        additional_domains=additional_domains_for_mongos,
    )

    if internal_auth:
        data = read_secret(namespace, f"{resource_name}-mongos-cert")
        create_secret(namespace, f"{resource_name}-mongos-clusterfile", data)


def create_x509_agent_tls_certs(issuer: str, namespace: str, name: str) -> str:
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

    full_certs = {}
    for agent in agents:
        agent_name = f"mms-{agent}-agent"
        spec["dnsNames"] = [agent_name]
        spec["commonName"] = agent_name
        secret = generate_cert(namespace, agent_name, agent_name, issuer, spec)
        agent_cert = KubernetesTester.read_secret(namespace, secret)
        full_certs[agent_name + "-pem"] = agent_cert["tls.crt"] + agent_cert["tls.key"]
    KubernetesTester.create_secret(namespace, "agent-certs", full_certs)


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
    secret = generate_cert(namespace, user_name, user_name, issuer, spec)
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
