"""
Certificate Custom Resource Definition.
"""

import collections
import copy
import random
import time
from datetime import datetime, timezone
from typing import Dict, Generator, List, Optional

import kubernetes
from kubeobject import CustomObject
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import (
    create_secret,
    delete_secret,
    kubetester,
    random_k8s_name,
    read_secret,
)
from kubetester.kubetester import KubernetesTester
from kubetester.phase import Phase
from opentelemetry import trace
from tests import test_logger
from tests.vaultintegration import (
    store_secret_in_vault,
    vault_namespace_name,
    vault_sts_name,
)

TRACER = trace.get_tracer("evergreen-agent")
logger = test_logger.get_test_logger(__name__)

ISSUER_CA_NAME = "ca-issuer"

SUBJECT = {
    # Organizational Units matches your namespace (to be overriden by test)
    "organizationalUnits": ["TO-BE-REPLACED"],
}

# Defines properties of a set of servers, like a Shard, or Replica Set holding config servers.
# This is almost equivalent to the StatefulSet created.
SetProperties = collections.namedtuple("SetProperties", ["name", "service", "replicas"])
SetPropertiesMultiCluster = collections.namedtuple(
    "SetProperties", ["name", "service", "replicas", "number_of_clusters"]
)


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
            if condition["reason"] == self.Reason and condition["status"] == "True" and condition["type"] == "Ready":
                return True

    def block_until_ready(self):
        while not self.is_ready():
            time.sleep(2)


class Certificate(CertificateType, WaitForConditions):
    Reason = "Ready"


IssuerType = CustomObject.define("Issuer", kind="Issuer", plural="issuers", group="cert-manager.io", version="v1")
ClusterIssuerType = CustomObject.define(
    "ClusterIssuer",
    kind="ClusterIssuer",
    plural="clusterissuers",
    group="cert-manager.io",
    version="v1",
)


class Issuer(IssuerType, WaitForConditions):
    Reason = "KeyPairVerified"


class ClusterIssuer(ClusterIssuerType, WaitForConditions):
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
    common_name: Optional[str] = None,
    clusterwide: bool = False,
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

    issuerRef = {"name": issuer, "kind": "Issuer"}
    if clusterwide:
        issuerRef["kind"] = "ClusterIssuer"

    cert["spec"] = {
        "dnsNames": dns_names,
        "secretName": secret_name,
        "issuerRef": issuerRef,
        "duration": "240h",
        "renewBefore": "120h",
        "usages": ["server auth", "client auth"],
    }

    # The use of the common name field has been deprecated since 2000 and is
    # discouraged from being used.
    # However, KMIP still enforces it :(
    if common_name is not None:
        cert["spec"]["commonName"] = common_name

    cert["spec"].update(spec)
    cert.api = kubernetes.client.CustomObjectsApi(api_client=api_client)
    cert.update()
    print(f"Waiting for certificate to become ready: {cert}")
    cert.block_until_ready()

    if secret_backend == "Vault":
        path = "secret/mongodbenterprise/"
        if vault_subpath is None:
            raise ValueError("When secret backend is Vault, a subpath must be specified")
        path += f"{vault_subpath}/{namespace}/{secret_name}"

        data = read_secret(namespace, secret_name)
        store_secret_in_vault(vault_namespace_name(), vault_sts_name(), data, path)
        cert.delete()
        delete_secret(namespace, secret_name)

    return secret_name


def rotate_cert(namespace, certificate_name, should_block_until_ready=False):
    cert = Certificate(name=certificate_name, namespace=namespace)
    cert.load()
    cert["spec"]["dnsNames"].append("foo")  # Append DNS to cert to rotate the certificate
    cert.update()
    if should_block_until_ready:
        cert.block_until_ready()


def create_tls_certs(
    issuer: str,
    namespace: str,
    resource_name: str,
    replicas: int = 3,
    replicas_cluster_distribution: Optional[List[int]] = None,
    service_name: str = None,
    spec: Optional[Dict] = None,
    secret_name: Optional[str] = None,
    additional_domains: Optional[List[str]] = None,
    secret_backend: Optional[str] = None,
    vault_subpath: Optional[str] = None,
    common_name: Optional[str] = None,
    process_hostnames: Optional[List[str]] = None,
    clusterwide: bool = False,
) -> str:
    """
    :param process_hostnames: set for TLS certificate to contain only given domains
    """
    if service_name is None:
        service_name = resource_name + "-svc"

    if spec is None:
        spec = dict()

    pod_dns = []
    pods = []
    if replicas_cluster_distribution is None:
        for pod_idx in range(replicas):
            if process_hostnames is not None:
                pod_dns.append(process_hostnames[pod_idx])
            else:
                pod_dns.append(f"{resource_name}-{pod_idx}.{service_name}.{namespace}.svc.cluster.local")
                pods.append(f"{resource_name}-{pod_idx}")
    else:
        for cluster_idx, pod_count in enumerate(replicas_cluster_distribution):
            if process_hostnames is not None:
                raise Exception("process_hostnames are not yet implemented for cluster_distribution argument")
            for pod_idx in range(pod_count or 0):
                pod_dns.append(f"{resource_name}-{cluster_idx}-{pod_idx}-svc.{namespace}.svc.cluster.local")
                pods.append(f"{resource_name}-{cluster_idx}-{pod_idx}")

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
        common_name=common_name,
        clusterwide=clusterwide,
    )
    return cert_secret_name


def create_ops_manager_tls_certs(
    issuer: str,
    namespace: str,
    om_name: str,
    secret_name: Optional[str] = None,
    secret_backend: Optional[str] = None,
    additional_domains: Optional[List[str]] = None,
    api_client: Optional[kubernetes.client.ApiClient] = None,
    clusterwide: bool = False,
) -> str:
    certs_secret_name = "certs-for-ops-manager"

    if secret_name is not None:
        certs_secret_name = secret_name

    domain = f"{om_name}-svc.{namespace}.svc.cluster.local"
    central_domain = f"{om_name}-central.{namespace}.svc.cluster.local"
    hostnames = [domain, central_domain]
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
        api_client=api_client,
        clusterwide=clusterwide,
    )


def create_vault_certs(namespace: str, issuer: str, vault_namespace: str, vault_name: str, secret_name: str):
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
    replicas_cluster_distribution: Optional[List[int]] = None,
    service_name: str = None,
    spec: Optional[Dict] = None,
    additional_domains: Optional[List[str]] = None,
    secret_backend: Optional[str] = None,
    vault_subpath: Optional[str] = None,
    process_hostnames: Optional[List[str]] = None,
    clusterwide: bool = False,
) -> str:
    """
    :param process_hostnames: set for TLS certificate to contain only given domains
    """
    cert_and_pod_names = create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=resource_name,
        replicas=replicas,
        replicas_cluster_distribution=replicas_cluster_distribution,
        service_name=service_name,
        spec=spec,
        additional_domains=additional_domains,
        secret_name=bundle_secret_name,
        secret_backend=secret_backend,
        vault_subpath=vault_subpath,
        process_hostnames=process_hostnames,
        clusterwide=clusterwide,
    )

    return cert_and_pod_names


def multi_cluster_service_fqdns(
    resource_name: str,
    namespace: str,
    external_domain: str,
    cluster_index: int,
    replicas: int,
) -> List[str]:
    service_fqdns = []

    for n in range(replicas):
        if external_domain is None:
            service_fqdns.append(f"{resource_name}-{cluster_index}-{n}-svc.{namespace}.svc.cluster.local")
        else:
            service_fqdns.append(f"{resource_name}-{cluster_index}-{n}.{external_domain}")

    return service_fqdns


def multi_cluster_external_service_fqdns(
    resource_name: str, namespace: str, cluster_index: int, replicas: int
) -> List[str]:
    service_fqdns = []

    for n in range(replicas):
        service_fqdns.append(f"{resource_name}-{cluster_index}-{n}-svc-external.{namespace}.svc.cluster.local")

    return service_fqdns


def create_x509_mongodb_tls_certs(
    issuer: str,
    namespace: str,
    resource_name: str,
    bundle_secret_name: str,
    replicas: int = 3,
    replicas_cluster_distribution: Optional[List[int]] = None,
    service_name: str = None,
    additional_domains: Optional[List[str]] = None,
    secret_backend: Optional[str] = None,
    vault_subpath: Optional[str] = None,
) -> str:
    spec = get_mongodb_x509_subject(namespace)

    return create_mongodb_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=resource_name,
        bundle_secret_name=bundle_secret_name,
        replicas=replicas,
        replicas_cluster_distribution=replicas_cluster_distribution,
        service_name=service_name,
        spec=spec,
        additional_domains=additional_domains,
        secret_backend=secret_backend,
        vault_subpath=vault_subpath,
    )


def get_mongodb_x509_subject(namespace):
    """
    x509 certificates need a subject, more here: https://wiki.corp.mongodb.com/display/MMS/E2E+Tests+Notes
    """
    subject = {
        "countries": ["US"],
        "provinces": ["NY"],
        "localities": ["NY"],
        "organizations": ["cluster.local-server"],
        "organizationalUnits": [namespace],
    }
    spec = {
        "subject": subject,
        "usages": [
            "digital signature",
            "key encipherment",
            "client auth",
            "server auth",
        ],
    }
    return spec


def get_agent_x509_subject(namespace):
    """
    x509 certificates need a subject, more here: https://wiki.corp.mongodb.com/display/MMS/E2E+Tests+Notes
    """
    agents = ["automation", "monitoring", "backup"]
    subject = {
        "countries": ["US"],
        "provinces": ["NY"],
        "localities": ["NY"],
        "organizations": ["cluster.local-agent"],
        "organizationalUnits": [namespace],
    }
    spec = {
        "subject": subject,
        "usages": ["digital signature", "key encipherment", "client auth"],
        "dnsNames": agents,
        "commonName": "mms-automation-agent",
    }
    return spec


def create_agent_tls_certs(
    issuer: str,
    namespace: str,
    name: str,
    secret_prefix: Optional[str] = None,
    secret_backend: Optional[str] = None,
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
    secret_name = "agent-certs" if secret_prefix is None else f"{secret_prefix}-{name}-agent-certs"
    secret = generate_cert(
        namespace=namespace,
        pod=[],
        dns=[],
        issuer=issuer,
        spec=spec,
        secret_name=secret_name,
        secret_backend=secret_backend,
        vault_subpath="database",
    )
    return secret


def create_sharded_cluster_certs(
    namespace: str,
    resource_name: str,
    shards: int,
    mongod_per_shard: int,
    config_servers: int,
    mongos: int,
    internal_auth: bool = False,
    x509_certs: bool = False,
    additional_domains: Optional[List[str]] = None,
    secret_prefix: Optional[str] = None,
    secret_backend: Optional[str] = None,
    shard_distribution: Optional[List[int]] = None,
    mongos_distribution: Optional[List[int]] = None,
    config_srv_distribution: Optional[List[int]] = None,
):
    cert_generation_func = create_mongodb_tls_certs
    if x509_certs:
        cert_generation_func = create_x509_mongodb_tls_certs

    for shard_idx in range(shards):
        additional_domains_for_shard = None
        if additional_domains is not None:
            additional_domains_for_shard = []
            for domain in additional_domains:
                if shard_distribution is None:
                    for pod_idx in range(mongod_per_shard):
                        additional_domains_for_shard.append(f"{resource_name}-{shard_idx}-{pod_idx}.{domain}")
                else:
                    for cluster_idx, pod_count in enumerate(shard_distribution):
                        for pod_idx in range(pod_count or 0):
                            additional_domains_for_shard.append(
                                f"{resource_name}-{shard_idx}-{cluster_idx}-{pod_idx}.{domain}"
                            )

        secret_name = f"{resource_name}-{shard_idx}-cert"
        if secret_prefix is not None:
            secret_name = secret_prefix + secret_name
        cert_generation_func(
            issuer=ISSUER_CA_NAME,
            namespace=namespace,
            resource_name=f"{resource_name}-{shard_idx}",
            bundle_secret_name=secret_name,
            replicas=mongod_per_shard,
            replicas_cluster_distribution=shard_distribution,
            service_name=resource_name + "-sh",
            additional_domains=additional_domains_for_shard,
            secret_backend=secret_backend,
        )
        if internal_auth:
            cert_generation_func(
                issuer=ISSUER_CA_NAME,
                namespace=namespace,
                resource_name=f"{resource_name}-{shard_idx}-clusterfile",
                bundle_secret_name=f"{resource_name}-{shard_idx}-clusterfile",
                replicas=mongod_per_shard,
                replicas_cluster_distribution=shard_distribution,
                service_name=resource_name + "-sh",
                additional_domains=additional_domains_for_shard,
                secret_backend=secret_backend,
            )

    additional_domains_for_config = None
    if additional_domains is not None:
        additional_domains_for_config = []
        for domain in additional_domains:
            if config_srv_distribution is None:
                for pod_idx in range(config_servers):
                    additional_domains_for_config.append(f"{resource_name}-config-{pod_idx}.{domain}")
            else:
                for cluster_idx, pod_count in enumerate(config_srv_distribution):
                    for pod_idx in range(pod_count or 0):
                        additional_domains_for_config.append(f"{resource_name}-config-{cluster_idx}-{pod_idx}.{domain}")

    secret_name = f"{resource_name}-config-cert"
    if secret_prefix is not None:
        secret_name = secret_prefix + secret_name
    cert_generation_func(
        issuer=ISSUER_CA_NAME,
        namespace=namespace,
        resource_name=resource_name + "-config",
        bundle_secret_name=secret_name,
        replicas=config_servers,
        replicas_cluster_distribution=config_srv_distribution,
        service_name=resource_name + "-cs",
        additional_domains=additional_domains_for_config,
        secret_backend=secret_backend,
    )
    if internal_auth:
        cert_generation_func(
            issuer=ISSUER_CA_NAME,
            namespace=namespace,
            resource_name=f"{resource_name}-config-clusterfile",
            bundle_secret_name=f"{resource_name}-config-clusterfile",
            replicas=config_servers,
            replicas_cluster_distribution=config_srv_distribution,
            service_name=resource_name + "-cs",
            additional_domains=additional_domains_for_config,
            secret_backend=secret_backend,
        )

    additional_domains_for_mongos = None
    if additional_domains is not None:
        additional_domains_for_mongos = []
        for domain in additional_domains:
            if mongos_distribution is None:
                for pod_idx in range(mongos):
                    additional_domains_for_mongos.append(f"{resource_name}-mongos-{pod_idx}.{domain}")
            else:
                for cluster_idx, pod_count in enumerate(mongos_distribution):
                    for pod_idx in range(pod_count or 0):
                        additional_domains_for_mongos.append(f"{resource_name}-mongos-{cluster_idx}-{pod_idx}.{domain}")

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
        replicas_cluster_distribution=mongos_distribution,
        additional_domains=additional_domains_for_mongos,
        secret_backend=secret_backend,
    )

    if internal_auth:
        cert_generation_func(
            issuer=ISSUER_CA_NAME,
            namespace=namespace,
            resource_name=f"{resource_name}-mongos-clusterfile",
            bundle_secret_name=f"{resource_name}-mongos-clusterfile",
            replicas=mongos,
            replicas_cluster_distribution=mongos_distribution,
            service_name=resource_name + "-sh",
            additional_domains=additional_domains_for_mongos,
            secret_backend=secret_backend,
        )


def create_x509_agent_tls_certs(issuer: str, namespace: str, name: str, secret_backend: Optional[str] = None) -> str:
    spec = get_agent_x509_subject(namespace)
    return generate_cert(
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
    body = client.CertificatesV1Api().read_certificate_signing_request_status(name)
    conditions = client.V1CertificateSigningRequestCondition(
        last_update_time=datetime.now(timezone.utc).astimezone(),
        message="This certificate was approved by E2E testing framework",
        reason="E2ETestingFramework",
        type="Approved",
    )

    body.status.conditions = [conditions]
    client.CertificatesV1Api().replace_certificate_signing_request_approval(name, body)


def create_x509_user_cert(issuer: str, namespace: str, path: str):
    user_name = "x509-testing-user"

    spec = {
        "usages": ["digital signature", "key encipherment", "client auth"],
        "commonName": user_name,
    }
    secret = generate_cert(
        namespace=namespace,
        pod=user_name,
        dns=user_name,
        issuer=issuer,
        spec=spec,
        secret_name="mongodbuser",
    )
    cert = KubernetesTester.read_secret(namespace, secret)
    with open(path, mode="w") as f:
        f.write(cert["tls.key"])
        f.write(cert["tls.crt"])
        f.flush()


def create_multi_cluster_x509_user_cert(
    multi_cluster_issuer: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    path: str,
):
    user_name = "x509-testing-user"
    spec = {
        "usages": ["digital signature", "key encipherment", "client auth"],
        "commonName": user_name,
    }
    secret = generate_cert(
        namespace=namespace,
        pod="tmp",
        dns=user_name,
        issuer=multi_cluster_issuer,
        api_client=central_cluster_client,
        spec=spec,
        multi_cluster_mode=True,
        secret_name="mongodbuser",
    )
    cert = read_secret(namespace, secret, api_client=central_cluster_client)
    with open(path, mode="w") as f:
        f.write(cert["tls.key"])
        f.write(cert["tls.crt"])
        f.flush()


def yield_existing_csrs(csr_names: List[str], timeout: int = 300) -> Generator[str, None, None]:
    """Returns certificate names as they start appearing in the Kubernetes API."""
    csr_names = csr_names.copy()
    total_csrs = len(csr_names)
    seen_csrs = 0
    stop_time = time.time() + timeout

    while len(csr_names) > 0 and time.time() < stop_time:
        csr = random.choice(csr_names)
        try:
            client.CertificatesV1Api().read_certificate_signing_request_status(csr)
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


@TRACER.start_as_current_span("assert_certificate_rotation")
def rotate_and_assert_certificates(mdb, namespace, certificate_name):
    """
    Verifies certificate rotation completes successfully.

    Rotates the specified certificate and validates that:
    1. Automation config version increases, as cert changes causes a new ac version
    2. All MongoDB processes reach the new goal version
    3. MongoDB instance returns/stays to Running state

    """

    old_ac_version = KubernetesTester.get_automation_config()["version"]
    rotate_cert(namespace, certificate_name, should_block_until_ready=True)

    # Create named function to check version and process status
    def check_version_increased():

        current_version = KubernetesTester.get_automation_config()["version"]
        version_increased = current_version > old_ac_version

        return version_increased

    timeout = 600
    KubernetesTester.wait_until(
        check_version_increased,
        timeout=timeout,
    )
    kubetester.wait_processes_ready()

    mdb.assert_reaches_phase(Phase.Running, timeout=1200)
