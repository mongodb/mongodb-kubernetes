# ----------------------------------------------------------------------------
# This file contains the implementation of the KMIP Server (PyKMIP) deployment
#
# The deployment has been outlined in the outdated Enterprise Kubernetes Operator
# guide, that might be found here:
# https://docs.google.com/document/d/12Y5h7XDFedcgpSIWRxMgcjZClL6kZdIwdxPRotkuKck/edit#
# -----------------------------------------------------------

from typing import Dict, Optional

from kubernetes import client
from kubetester import (
    create_or_update_configmap,
    create_or_update_secret,
    create_or_update_service,
    create_statefulset,
    read_configmap,
    read_secret,
)
from kubetester.certs import create_tls_certs
from kubetester.kubetester import KubernetesTester


class KMIPDeployment(object):
    """
    A KMIP Server deployment class. Deploys PyKMIP in the cluster.
    """

    def __init__(self, namespace, issuer, root_cert_secret, ca_configmap: str):
        self.namespace = namespace
        self.issuer = issuer
        self.root_cert_secret = root_cert_secret
        self.ca_configmap = ca_configmap
        self.statefulset_name = "kmip"
        self.labels = {
            "app": "kmip",
        }

    def deploy(self):
        """
        Deploys a PyKMIP Server and returns the name of the deployed StatefulSet.
        """
        service_name = f"{self.statefulset_name}-svc"

        cert_secret_name = self._create_tls_certs_kmip(
            self.issuer,
            self.namespace,
            self.statefulset_name,
            "kmip-certs",
            1,
            service_name,
        )

        create_or_update_service(
            self.namespace,
            service_name,
            cluster_ip=None,
            ports=[client.V1ServicePort(name="kmip", port=5696)],
            selector=self.labels,
        )

        self._create_kmip_config_map(self.namespace, "kmip-config", self._default_configuration())

        create_statefulset(
            self.namespace,
            self.statefulset_name,
            service_name,
            self.labels,
            containers=[
                client.V1Container(
                    # We need this awkward copy step as PyKMIP uses /etc/pykmip as a tmp directory. When booting up
                    # it stores there some intermediate configuration files. So it must have write access to the whole
                    # /etc/pykmip directory. Very awkward...
                    args=[
                        "bash",
                        "-c",
                        "cp /etc/pykmip-conf/server.conf /etc/pykmip/server.conf & /tmp/configure.sh & mkdir -p /var/log/pykmip & touch /var/log/pykmip/server.log & tail -f /var/log/pykmip/server.log",
                    ],
                    name="kmip",
                    image="beergeek1679/pykmip:0.6.0",
                    image_pull_policy="IfNotPresent",
                    ports=[
                        client.V1ContainerPort(
                            container_port=5696,
                            name="kmip",
                        )
                    ],
                    volume_mounts=[
                        client.V1VolumeMount(
                            name="certs",
                            mount_path="/data/pki",
                            read_only=True,
                        ),
                        client.V1VolumeMount(
                            name="config",
                            mount_path="/etc/pykmip-conf",
                            read_only=True,
                        ),
                    ],
                )
            ],
            volumes=[
                client.V1Volume(
                    name="certs",
                    secret=client.V1SecretVolumeSource(
                        secret_name=cert_secret_name,
                    ),
                ),
                client.V1Volume(
                    name="config",
                    config_map=client.V1ConfigMapVolumeSource(name="kmip-config"),
                ),
            ],
        )
        return self

    def status(self):
        return KMIPDeploymentStatus(self)

    def _create_tls_certs_kmip(
        self,
        issuer: str,
        namespace: str,
        resource_name: str,
        bundle_secret_name: str,
        replicas: int = 3,
        service_name: str = None,
        spec: Optional[Dict] = None,
    ) -> str:
        ca = read_configmap(namespace, self.ca_configmap)
        cert_secret_name = create_tls_certs(
            issuer,
            namespace,
            resource_name,
            replicas=replicas,
            service_name=service_name,
            spec=spec,
            additional_domains=[service_name],
        )
        secret = read_secret(namespace, cert_secret_name)
        create_or_update_secret(
            namespace,
            bundle_secret_name,
            {
                "server.key": secret["tls.key"],
                "server.cert": secret["tls.crt"],
                "ca.cert": ca["ca-pem"],
            },
        )

        return bundle_secret_name

    def _default_configuration(self) -> Dict:
        return {
            "hostname": "kmip-0",
            "port": 5696,
            "certificate_path": "/data/pki/server.cert",
            "key_path": "/data/pki/server.key",
            "ca_path": "/data/pki/ca.cert",
            "auth_suite": "TLS1.2",
            "enable_tls_client_auth": True,
            "policy_path": "/data/policies",
            "logging_level": "DEBUG",
            "database_path": "/data/db/pykmip.db",
        }

    def _create_kmip_config_map(self, namespace: str, name: str, config_dict: Dict) -> None:
        """
        _create_configuration_config_map converts a dictionary of options into the server.conf
        file that the kmip server uses to start.
        """
        equals_separated = [k + "=" + str(v) for (k, v) in config_dict.items()]
        config_file_contents = "[server]\n" + "\n".join(equals_separated)
        create_or_update_configmap(
            namespace,
            name,
            {
                "server.conf": config_file_contents,
            },
        )


class KMIPDeploymentStatus:
    """
    A class designed to check the KMIP Server deployment status.
    """

    def __init__(self, deployment: KMIPDeployment):
        self.deployment = deployment

    def assert_is_running(self):
        """
        Waits and assert if the KMIP server is running.
        :return: raises an error if the server is not running within the timeout.
        """
        KubernetesTester.wait_for_condition_stateful_set(
            self.deployment.namespace,
            self.deployment.statefulset_name,
            "status.current_replicas",
            1,
        )
