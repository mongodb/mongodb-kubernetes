from pytest import fixture, mark

from kubetester.kubetester import KubernetesTester, fixture as _fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.certs import Certificate, Issuer

import time


@fixture("module")
def domain(namespace: str):
    return "om-with-https-svc.{}.svc.cluster.local".format(namespace)


@fixture("module")
def issuer(namespace: str):
    # Creates the Secret with the issuer CA
    issuer_data = {
        "tls.key": open(_fixture("ca-tls.key")).read(),
        "tls.crt": open(_fixture("ca-tls.crt")).read(),
    }
    KubernetesTester.create_secret(namespace, "ca-key-pair", issuer_data)

    # And then creates the Issuer
    issuer = Issuer(name="ca-issuer", namespace=namespace)
    issuer["spec"] = {"ca": {"secretName": "ca-key-pair"}}
    issuer.create().block_until_ready()

    return "ca-issuer"


@fixture("module")
def ops_manager_cert(domain: str, namespace: str, issuer: str):
    cert = Certificate(name="om-https-cert", namespace=namespace)
    cert["spec"] = {
        "dnsNames": [domain],
        "secretName": "om-https-cert-secret",
        "issuerRef": {"name": issuer},
        "duration": "2160h",  # 90d
        "renewBefore": "360h",  # 15d
    }
    cert.create().block_until_ready()

    https_cert = KubernetesTester.read_secret(namespace, "om-https-cert-secret")
    data = {"server.pem": https_cert["tls.key"] + https_cert["tls.crt"]}

    # Cert and Key file need to be merged into its own PEM file.
    KubernetesTester.create_secret(namespace, "certs-for-ops-manager", data)


@fixture("module")
def ops_manager(domain: str, namespace: str, ops_manager_cert) -> MongoDBOpsManager:
    _ = ops_manager_cert

    om = MongoDBOpsManager.from_yaml(
        _fixture("om_https_enabled.yaml"), namespace=namespace
    )
    return om.create()


@fixture("module")
def config_map_custom_ca(namespace: str):
    https_cert = KubernetesTester.read_secret(namespace, "om-https-cert-secret")
    data = {"mms-ca.crt": https_cert["ca.crt"]}

    KubernetesTester.create_configmap(namespace, "custom-ca", data)


@fixture("module")
def replicaset(ops_manager: MongoDBOpsManager, namespace: str):
    resource = MongoDB.from_yaml(
        _fixture("replica-set.yaml"), namespace=namespace
    ).configure(ops_manager, namespace)

    return resource.create()


@mark.e2e_om_ops_manager_https_enabled
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.assert_reaches_phase(Phase.Running, timeout=900)

    assert ops_manager["status"]["opsManager"]["url"].startswith("https://")
    assert ops_manager["status"]["opsManager"]["url"].endswith(":8443")


@mark.e2e_om_ops_manager_https_enabled
def test_project_is_configured_with_custom_ca(
    ops_manager: MongoDBOpsManager, namespace: str
):
    configmap = ops_manager.get_or_create_mongodb_connection_config_map(
        "my-replica-set", namespace
    )

    data = {
        "sslMMSCAConfigMap": "custom-ca",
    }
    KubernetesTester.update_configmap(namespace, configmap, data)


@mark.e2e_om_ops_manager_https_enabled
def test_mongodb_replicaset_uses_custom_ca(replicaset: MongoDB, config_map_custom_ca):
    _ = config_map_custom_ca

    replicaset.assert_reaches_phase(Phase.Running)
