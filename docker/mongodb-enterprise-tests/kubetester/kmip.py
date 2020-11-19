from typing import Optional, Dict
from kubetester import (
    create_secret,
    read_secret,
    create_service,
    create_statefulset,
    create_configmap,
)
from kubetester.certs import create_tls_certs
from kubernetes import client


def create_kmip_server(issuer: str, namespace: str) -> str:
    """
    Creates a KMIP server as outlined in this doc https://docs.google.com/document/d/12Y5h7XDFedcgpSIWRxMgcjZClL6kZdIwdxPRotkuKck/edit#
    """
    statefulset_name = "kmip"
    service_name = f"{statefulset_name}-svc"

    cert_secret_name = _create_tls_certs_kmip(
        issuer, namespace, statefulset_name, "kmip-certs", replicas=1
    )

    create_service(
        namespace,
        service_name,
        cluster_ip=None,
        ports=[client.V1ServicePort(name="kmip", port=5696)],
    )

    _create_kmip_config_map(namespace, "kmip-config", _default_configuration())

    labels = {
        "app": "kmip",
    }

    create_statefulset(
        namespace,
        statefulset_name,
        service_name,
        labels,
        containers=[
            client.V1Container(
                name="kmip",
                image="beergeek1679/pykmip:0.4.0",
                image_pull_policy="IfNotPresent",
                ports=[client.V1ContainerPort(container_port=5696, name="kmip",)],
                volume_mounts=[
                    client.V1VolumeMount(
                        name="certs", mount_path="/data/pki", read_only=True,
                    ),
                    client.V1VolumeMount(
                        name="config", mount_path="/etc/pykmip", read_only=True,
                    ),
                ],
            )
        ],
        volumes=[
            client.V1Volume(
                name="certs",
                secret=client.V1SecretVolumeSource(secret_name=cert_secret_name,),
            ),
            client.V1Volume(
                name="config",
                config_map=client.V1ConfigMapVolumeSource(name="kmip-config"),
            ),
        ],
    )
    return statefulset_name


def _create_tls_certs_kmip(
    issuer: str,
    namespace: str,
    resource_name: str,
    bundle_secret_name: str,
    replicas: int = 3,
    service_name: str = None,
    spec: Optional[Dict] = None,
) -> str:
    cert_and_pod_names = create_tls_certs(
        issuer, namespace, resource_name, replicas, service_name, spec
    )

    _, cert_secret_name = cert_and_pod_names.items()[0]
    secret = read_secret(namespace, cert_secret_name)
    ca_secret = read_secret(namespace, "ca-key-pair")
    create_secret(
        namespace,
        bundle_secret_name,
        {
            "server.key": secret["tls.key"],
            "server.cert": secret["tls.crt"],
            "ca.cert": ca_secret["tls.crt"],
        },
    )
    return bundle_secret_name


def _default_configuration() -> Dict:
    return {
        "hostname": "kmip-0",
        "port": 5696,
        "certificate_path": "/data/pki/server.cert",
        "key_path": "/data/pki/server.key",
        "ca_path": "/data/pki/ca.cert",
        "auth_suite": "TLS1.2",
        "enable_tls_client_auth": True,
        "logging_level": "DEBUG",
        "database_path": "database_path=/data/db/pykmip.db",
    }


def _create_kmip_config_map(namespace: str, name: str, config_dict: Dict) -> None:
    """
    _create_configuration_config_map converts a dictionary of options into the server.conf
    file that the kmip server uses to start.
    """
    equals_separated = [k + "=" + str(v) for (k, v) in config_dict.items()]
    config_file_contents = "[server]\n" + "\n".join(equals_separated)
    create_configmap(namespace, name, {"server.conf": config_file_contents,})
