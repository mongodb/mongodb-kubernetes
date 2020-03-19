from pytest import fixture, mark

from kubetester.kubetester import KubernetesTester, fixture as _fixture
from kubetester.mongodb import MongoDBOpsManager, MongoDB, Phase
from kubetester.certs import Certificate, Issuer

import time


@fixture("module")
def domain(namespace: str):
    return "om-pod-spec-with-custom-ca-svc.{}.svc.cluster.local".format(namespace)


@fixture("module")
def issuer(namespace: str):
    # Creates the Secret with the issuer CA. This is done using the ca-tls.key
    # and ca-tls.crt files created manually.
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

    # The certificate generated includes 3 entries:
    # * tls.crt and tls.key used to setup TLS on a server
    # * ca.crt used on a client to authenticate to a server
    #   This is the same certificate used to sign the newly
    #   generated certs.
    https_cert = KubernetesTester.read_secret(namespace, "om-https-cert-secret")
    data = {"server.pem": https_cert["tls.key"] + https_cert["tls.crt"]}

    # Cert and Key file need to be merged into its own PEM file.
    KubernetesTester.create_secret(namespace, "custom-ca-for-ops-manager", data)


@fixture("module")
def ops_manager(domain: str, namespace: str, ops_manager_cert) -> MongoDBOpsManager:
    _ = ops_manager_cert

    om = MongoDBOpsManager.from_yaml(_fixture("om_custom_ca.yaml"), namespace=namespace)
    return om.create()


@fixture("module")
def config_map_custom_ca(namespace: str):
    """Creates the ConfigMap containing the mms-ca.crt file.

    The "ca.crt" entry is included in the Secret by cert-manager, as a mechanism to share
    the certificate that will verify the generated tls.crt.
    """
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


@mark.e2e_om_ops_manager_https_enabled
def test_om_pod_spec(ops_manager: MongoDBOpsManager):
    sts = ops_manager.get_statefulset()
    assert len(sts.spec.template.spec.containers) == 1


@mark.e2e_om_ops_manager_https_enabled
def test_om_container_override(ops_manager: MongoDBOpsManager):
    sts = ops_manager.get_statefulset()
    om_container = sts.spec.template.spec.containers[0].to_dict()
    expected_spec = {
        "name": "mongodb-ops-manager",
        "volume_mounts": [
            {
                "name": "om-cert",
                "mount_path": "/pod-spec-mount",
                "sub_path": None,
                "sub_path_expr": None,
                "mount_propagation": None,
                "read_only": None,
            },
            {
                "name": "mongodb-versions",
                "mount_path": "/mongodb-ops-manager/mongodb-releases",
                "sub_path": None,
                "sub_path_expr": None,
                "mount_propagation": None,
                "read_only": None,
            },
            {
                "name": "gen-key",
                "mount_path": "/mongodb-ops-manager/.mongodb-mms",
                "sub_path": None,
                "sub_path_expr": None,
                "mount_propagation": None,
                "read_only": True,
            },
        ],
    }
    for k in expected_spec:
        assert om_container[k] == expected_spec[k]

    assert len(sts.spec.template.spec.volumes) == 3

    assert sts.spec.template.spec.volumes[0].name == "om-cert"
    assert getattr(sts.spec.template.spec.volumes[0], "secret")

    assert sts.spec.template.spec.volumes[1].name == "mongodb-versions"
    assert getattr(sts.spec.template.spec.volumes[1], "empty_dir")

    assert sts.spec.template.spec.volumes[2].name == "gen-key"
    assert (
        sts.spec.template.spec.volumes[2].secret.secret_name
        == "om-pod-spec-with-custom-ca-gen-key"
    )


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
