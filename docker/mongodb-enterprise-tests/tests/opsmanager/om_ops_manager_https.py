from pytest import fixture, mark

import time

from kubetester.kubetester import KubernetesTester, fixture as _fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.certs import Certificate, Issuer


@fixture("module")
def domain(namespace: str):
    return "om-with-https-svc.{}.svc.cluster.local".format(namespace)


def generate_cert(namespace, pod, pod_dns, issuer):
    cert = Certificate(namespace=namespace, name=pod)
    cert["spec"] = {
        "dnsNames": [pod, pod_dns],
        "secretName": pod,
        "issuerRef": {"name": issuer},
        "duration": "240h",
        "renewBefore": "120h",
        "usages": ["server auth", "client auth"],
    }
    return cert.create().block_until_ready()


@fixture("module")
def appdb_certs(namespace: str, issuer):
    resource_name = "om-with-https-db"
    pod_fqdn_fstring = "{resource_name}-{index}.{resource_name}-svc.{namespace}.svc.cluster.local".format(
        resource_name=resource_name, namespace=namespace, index="{}",
    )

    data = {}
    for idx in range(3):
        pod_dns = pod_fqdn_fstring.format(idx)
        pod_name = f"{resource_name}-{idx}"
        cert = generate_cert(namespace, pod_name, pod_dns, issuer)
        secret = KubernetesTester.read_secret(namespace, pod_name)
        data[pod_name + "-pem"] = secret["tls.key"] + secret["tls.crt"]

    KubernetesTester.create_secret(namespace, "certs-for-appdb", data)

    return "certs-for-appdb"


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

    return "certs-for-ops-manager"


@fixture("module")
def ops_manager(
    domain: str, namespace: str, issuer_ca_configmap: str, appdb_certs: str
) -> MongoDBOpsManager:
    om = MongoDBOpsManager.from_yaml(
        _fixture("om_https_enabled.yaml"), namespace=namespace
    )
    om["spec"]["applicationDatabase"]["security"] = {
        "tls": {"ca": issuer_ca_configmap, "secretRef": {"name": appdb_certs}}
    }
    return om.create()


@fixture("module")
def replicaset0(ops_manager: MongoDBOpsManager, namespace: str):
    """First replicaset to be created before Ops Manager is configured with HTTPS."""
    resource = MongoDB.from_yaml(
        _fixture("replica-set.yaml"), name="replicaset0", namespace=namespace
    ).configure(ops_manager, "replicaset0")

    return resource.create()


@fixture("module")
def replicaset1(ops_manager: MongoDBOpsManager, namespace: str):
    """Second replicaset to be created when Ops Manager was restarted with HTTPS."""
    resource = MongoDB.from_yaml(
        _fixture("replica-set.yaml"), name="replicaset1", namespace=namespace
    ).configure(ops_manager, "replicaset1")

    return resource.create()


@mark.e2e_om_ops_manager_https_enabled
def test_om_created(ops_manager: MongoDBOpsManager):
    """Ops Manager is started over plain HTTP."""
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    # 'authentication' is not show for applicationDatabase
    assert (
        "authentication" not in ops_manager["spec"]["applicationDatabase"]["security"]
    )

    assert ops_manager.om_status().get_url().startswith("http://")
    assert ops_manager.om_status().get_url().endswith(":8080")


@mark.e2e_om_ops_manager_https_enabled
def test_replica_set_over_non_https_ops_manager(replicaset0: MongoDB):
    """First replicaset is started over non-HTTPS Ops Manager."""
    replicaset0.assert_reaches_phase(Phase.Running)
    replicaset0.assert_connectivity()


@mark.e2e_om_ops_manager_https_enabled
def test_enable_https_on_opsmanager(
    ops_manager: MongoDBOpsManager, ops_manager_cert: str
):
    """Ops Manager is restarted with HTTPS enabled."""
    ops_manager["spec"]["security"] = {"tls": {"secretRef": {"name": ops_manager_cert}}}
    ops_manager.update()

    ops_manager.om_status().assert_abandons_phase(Phase.Running)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    assert ops_manager.om_status().get_url().startswith("https://")
    assert ops_manager.om_status().get_url().endswith(":8443")


@mark.e2e_om_ops_manager_https_enabled
def test_project_is_configured_with_custom_ca(
    ops_manager: MongoDBOpsManager, namespace: str, issuer_ca_configmap: str,
):
    """Both projects are configured with the new HTTPS enabled Ops Manager."""
    project1 = ops_manager.get_or_create_mongodb_connection_config_map(
        "replicaset0", "replicaset0"
    )
    project2 = ops_manager.get_or_create_mongodb_connection_config_map(
        "replicaset1", "replicaset1"
    )

    data = {
        "sslMMSCAConfigMap": issuer_ca_configmap,
    }
    KubernetesTester.update_configmap(namespace, project1, data)
    KubernetesTester.update_configmap(namespace, project2, data)

    # Give a few seconds for the operator to catch the changes on
    # the project ConfigMaps
    time.sleep(10)


@mark.e2e_om_ops_manager_https_enabled
def test_mongodb_replicaset_over_https_ops_manager(
    replicaset0: MongoDB, replicaset1: MongoDB
):
    """Both replicasets get to running state and are reachable.
    Note that 'replicaset1' is created just now."""
    replicaset0.assert_reaches_phase(Phase.Running, timeout=360)
    replicaset0.assert_connectivity()

    replicaset1.assert_reaches_phase(Phase.Running, timeout=360)
    replicaset1.assert_connectivity()
