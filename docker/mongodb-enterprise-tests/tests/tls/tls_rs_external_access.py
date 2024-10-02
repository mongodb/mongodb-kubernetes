import pytest
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB, Phase


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    mdb_resource = "test-tls-base-rs-external-access"
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        mdb_resource,
        f"{mdb_resource}-cert",
        replicas=4,
        additional_domains=[
            "*.website.com",
        ],
    )


@pytest.fixture(scope="module")
def server_certs_multiple_horizons(issuer: str, namespace: str):
    mdb_resource = "test-tls-rs-external-access-multiple-horizons"
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        mdb_resource,
        f"{mdb_resource}-cert",
        replicas=4,
        additional_domains=[
            "mdb0-test-1.website.com",
            "mdb0-test-2.website.com",
            "mdb1-test-1.website.com",
            "mdb1-test-2.website.com",
            "mdb2-test-1.website.com",
            "mdb2-test-2.website.com",
        ],
    )


@pytest.fixture(scope="module")
def mdb(namespace: str, server_certs: str, issuer_ca_configmap: str, custom_mdb_version: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("test-tls-base-rs-external-access.yaml"), namespace=namespace)
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    res.set_version(custom_mdb_version)
    return res.update()


@pytest.fixture(scope="module")
def mdb_multiple_horizons(namespace: str, server_certs_multiple_horizons: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(
        load_fixture("test-tls-rs-external-access-multiple-horizons.yaml"),
        namespace=namespace,
    )
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return res.create()


@pytest.mark.e2e_tls_rs_external_access
class TestReplicaSetWithExternalAccess(KubernetesTester):
    def test_replica_set_running(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=240)

    @skip_if_local
    def test_can_still_connect(self, mdb: MongoDB, ca_path: str):
        tester = mdb.tester(use_ssl=True, ca_path=ca_path)
        tester.assert_connectivity()

    def test_automation_config_is_right(self):
        ac = self.get_automation_config()
        members = ac["replicaSets"][0]["members"]
        horizon_names = [m["horizons"] for m in members]
        assert horizon_names == [
            {"test-horizon": "mdb0-test.website.com:1337"},
            {"test-horizon": "mdb1-test.website.com:1337"},
            {"test-horizon": "mdb2-test.website.com:1337"},
        ]

    @skip_if_local
    def test_has_right_certs(self):
        """
        Check that mongod processes behind the replica set service are
        serving the right certificates.
        """
        host = f"test-tls-base-rs-external-access-svc.{self.namespace}.svc"
        assert any(san.endswith(".website.com") for san in self.get_mongo_server_sans(host))


@pytest.mark.e2e_tls_rs_external_access
def test_scale_up_replica_set(mdb: MongoDB):
    mdb.load()
    mdb["spec"]["members"] = 4
    mdb["spec"]["connectivity"]["replicaSetHorizons"] = [
        {"test-horizon": "mdb0-test.website.com:1337"},
        {"test-horizon": "mdb1-test.website.com:1337"},
        {"test-horizon": "mdb2-test.website.com:1337"},
        {"test-horizon": "mdb3-test.website.com:1337"},
    ]
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=240)


@pytest.mark.e2e_tls_rs_external_access
def tests_invalid_cert(mdb: MongoDB):
    mdb.load()
    mdb["spec"]["connectivity"]["replicaSetHorizons"] = [
        {"test-horizon": "mdb0-test.website.com:1337"},
        {"test-horizon": "mdb1-test.website.com:1337"},
        {"test-horizon": "mdb2-test.website.com:1337"},
        {"test-horizon": "mdb5-test.wrongwebsite.com:1337"},
    ]
    mdb.update()
    mdb.assert_reaches_phase(
        Phase.Failed,
        timeout=240,
    )


@pytest.mark.e2e_tls_rs_external_access
def test_can_remove_horizons(mdb: MongoDB):
    mdb.load()
    mdb["spec"]["connectivity"]["replicaSetHorizons"] = []
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=240)


@pytest.mark.e2e_tls_rs_external_access
class TestReplicaSetWithNoTLSDeletion(KubernetesTester):
    """
    delete:
      file: test-tls-base-rs-external-access.yaml
      wait_until: mongo_resource_deleted_no_om
      timeout: 240
    """

    def test_deletion(self):
        assert True


@pytest.mark.e2e_tls_rs_external_access
class TestReplicaSetWithMultipleHorizons(KubernetesTester):
    def test_replica_set_running(self, mdb_multiple_horizons: MongoDB):
        mdb_multiple_horizons.assert_reaches_phase(Phase.Running, timeout=240)

    def test_automation_config_is_right(self):
        ac = self.get_automation_config()
        members = ac["replicaSets"][0]["members"]
        horizons = [m["horizons"] for m in members]
        assert horizons == [
            {
                "test-horizon-1": "mdb0-test-1.website.com:1337",
                "test-horizon-2": "mdb0-test-2.website.com:2337",
            },
            {
                "test-horizon-1": "mdb1-test-1.website.com:1338",
                "test-horizon-2": "mdb1-test-2.website.com:2338",
            },
            {
                "test-horizon-1": "mdb2-test-1.website.com:1339",
                "test-horizon-2": "mdb2-test-2.website.com:2339",
            },
        ]


@pytest.mark.e2e_tls_rs_external_access
class TestReplicaSetDeleteMultipleHorizon(KubernetesTester):
    """
    delete:
      file: test-tls-rs-external-access-multiple-horizons.yaml
      wait_until: mongo_resource_deleted_no_om
      timeout: 240
    """

    def test_deletion(self):
        assert True
