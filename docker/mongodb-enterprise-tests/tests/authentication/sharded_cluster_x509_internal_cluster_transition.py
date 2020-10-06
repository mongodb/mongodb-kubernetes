from kubetester import MongoDB
from kubetester import find_fixture
from kubetester.certs import approve_certificate, yield_existing_csrs
from kubetester.mongodb import Phase
from kubetester.omtester import get_sc_cert_names
from pytest import mark, fixture


@fixture(scope="module")
def sharded_cluster(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-x509-internal-cluster-auth-transition.yaml"),
        namespace=namespace,
    )

    resource["spec"]["security"] = {
        "tls": {"enabled": True},
        "authentication": {"enabled": True, "modes": ["X509"]},
    }

    yield resource.create()
    resource.delete()


@mark.e2e_sharded_cluster_internal_cluster_transition
def test_create_resource(sharded_cluster: MongoDB):
    # Not all certificates have been approved by Kubernetes CA
    sharded_cluster.assert_reaches_phase(
        Phase.Pending,
        msg_regexp="Not all certificates have been approved by Kubernetes CA.*",
    )


@mark.e2e_sharded_cluster_internal_cluster_transition
def test_certificates_approved(sharded_cluster: MongoDB):
    csr_names = get_sc_cert_names(
        sharded_cluster.name,
        sharded_cluster.namespace,
        num_shards=2,
        members=3,
        config_members=1,
        num_mongos=1,
        with_agent_certs=True,
    )
    for cert in yield_existing_csrs(csr_names):
        approve_certificate(cert)

    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_sharded_cluster_internal_cluster_transition
def test_enable_internal_cluster_authentication(sharded_cluster: MongoDB):
    sharded_cluster.load()
    sharded_cluster["spec"]["security"]["authentication"]["internalCluster"] = "X509"
    sharded_cluster.update()

    sharded_cluster.assert_reaches_phase(
        Phase.Pending,
        msg_regexp="Not all internal cluster authentication certs have been approved by Kubernetes CA.*",
    )
    csr_names = get_sc_cert_names(
        sharded_cluster.name,
        sharded_cluster.namespace,
        num_shards=2,
        members=3,
        config_members=1,
        num_mongos=1,
        with_internal_auth_certs=True,
    )

    for cert in yield_existing_csrs(csr_names):
        approve_certificate(cert)

    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=2400)
