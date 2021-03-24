import pytest
from kubetester.kubetester import fixture as load_fixture
from kubetester.certs import Certificate, ISSUER_CA_NAME
from kubetester.mongodb import MongoDB, Phase
from kubetester import create_secret, read_secret


def generate_cert(namespace, name, usages=None, subject=None, san=None):
    if usages is None:
        usages = ["server auth", "client auth"]

    if subject is None:
        subject = {}

    if san is None:
        san = [name]

    cert = Certificate(namespace=namespace, name=name + "-cert")
    cert["spec"] = {
        "secretName": name + "-cert-secret",
        "issuerRef": {"name": ISSUER_CA_NAME},
        "duration": "240h",
        "renewBefore": "120h",
        "usages": usages,
        "commonName": name,
        "subject": subject,
        "dnsNames": san,
    }
    cert.create().block_until_ready()

    return name + "-cert-secret"


def generate_server_cert(namespace, name, san):
    return generate_cert(namespace, name, san=san)


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    """Creates one 'test-x509-rs-cert' Secret with server TLS certs for 3 RS members. """
    resource_name = "test-tls-base-rs"
    pod_fqdn_fstring = "{resource_name}-{index}.{resource_name}-svc.{namespace}.svc.cluster.local".format(
        resource_name=resource_name,
        namespace=namespace,
        index="{}",
    )
    data = {}
    for i in range(3):
        pod_dns = pod_fqdn_fstring.format(i)
        pod_name = f"{resource_name}-{i}"
        cert = generate_server_cert(namespace, pod_name, [pod_dns, pod_name])
        secret = read_secret(namespace, cert)
        data[pod_name + "-pem"] = secret["tls.key"] + secret["tls.crt"]

    create_secret(namespace, f"{resource_name}-cert", data)

    return f"{resource_name}-cert"


@pytest.fixture(scope="module")
def replica_set(namespace: str, server_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("test-tls-base-rs.yaml"), namespace=namespace)
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap

    # Set this ReplicaSet to allowSSL mode
    # this is the only mode that can go to "disabled" state.
    res["spec"]["additionalMongodConfig"] = {"net": {"ssl": {"mode": "allowSSL"}}}

    return res.create()


@pytest.mark.e2e_disable_tls_scale_up
def test_rs_is_running(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_disable_tls_scale_up
def test_tls_is_disabled_and_scaled_up(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["members"] = 5

    replica_set.update()


@pytest.mark.e2e_disable_tls_scale_up
def test_tls_is_disabled_and_scaled_up(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["security"]["tls"]["enabled"] = False
    del replica_set["spec"]["additionalMongodConfig"]

    replica_set.update()

    # timeout is longer because the operator first needs to
    # disable TLS and then, scale down one by one.
    replica_set.assert_reaches_phase(Phase.Running, timeout=800)
