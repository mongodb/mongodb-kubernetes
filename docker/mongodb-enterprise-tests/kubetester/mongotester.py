import random
import string
import time
import ssl

import pymongo
from kubetester import kubetester
from kubetester.kubetester import KubernetesTester
from pymongo.errors import ServerSelectionTimeoutError, OperationFailure
from pytest import fail
from typing import List

TEST_DB = "test-db"
TEST_COLLECTION = "test-collection"


class MongoTester:
    """ MongoTester is a general abstraction to work with mongo database. It incapsulates the client created in
    the constructor. All general methods non-specific to types of mongodb topologies should reside here. """

    def __init__(self, connection_string: str, ssl: bool):
        # SSL is set to true by default if using mongodb+srv, it needs to be explicitely set to false
        # https://docs.mongodb.com/manual/reference/program/mongo/index.html#cmdoption-mongo-host
        options = {"ssl": ssl}
        if ssl:
            options["ssl_ca_certs"] = kubetester.SSL_CA_CERT

        self.cnx_string = connection_string
        self.client = pymongo.MongoClient(connection_string, **options)

    def assert_connectivity(self, attempts: int = 5):
        """ Trivial check to make sure mongod is alive """
        assert attempts > 0
        while True:
            attempts -= 1
            try:
                self.client.admin.command("ismaster")
            except ServerSelectionTimeoutError:
                if attempts == 0:
                    raise
                time.sleep(5)
            else:
                break

    def assert_no_connection(self):
        try:
            self.assert_connectivity()
            fail()
        except ServerSelectionTimeoutError:
            pass

    def assert_version(self, expected_version: str):
        assert self.client.admin.command("buildInfo")["version"] == expected_version

    def assert_data_size(self, expected_count, test_collection=TEST_COLLECTION):
        assert self.client[TEST_DB][test_collection].count() == expected_count

    def assert_is_enterprise(self):
        assert "enterprise" in self.client.admin.command("buildInfo")["modules"]

    def assert_scram_sha_authentication(
        self,
        username: str,
        password: str,
        auth_mechanism: str,
        attempts: int = 5,
        ssl: bool = False,
    ) -> None:
        assert attempts > 0
        assert auth_mechanism in {"SCRAM-SHA-256", "SCRAM-SHA-1"}

        for i in reversed(range(attempts)):
            try:
                self._authenticate_with_scram(
                    username, password, auth_mechanism=auth_mechanism, ssl=ssl
                )
                return
            except OperationFailure as e:
                if i == 0:
                    fail(
                        msg=f"unable to authenticate after {attempts} attempts with error: {e}"
                    )
                time.sleep(5)

    def assert_scram_sha_authentication_fails(
        self, username: str, password: str, retries: int = 5, **kwargs
    ):
        """
        If a password has changed, it could take some time for the user changes to propagate, meaning
        this could return true if we make a CRD change and immediately try to auth as the old user
        which still exists. When we change a password, we should eventually no longer be able to auth with
        that user's credentials.
        """
        for i in range(retries):
            try:
                self._authenticate_with_scram(username, password, **kwargs)
            except OperationFailure:
                return
            time.sleep(5)
        fail(
            f"was still able to authenticate with username={username} password={password} after {retries} attempts"
        )

    def _authenticate_with_scram(
        self, username: str, password: str, auth_mechanism: str, ssl: bool = False
    ):
        options = {
            "ssl": ssl,
            "authMechanism": auth_mechanism,
            "password": password,
            "username": username,
        }
        if ssl:
            options["ssl_ca_certs"] = kubetester.SSL_CA_CERT

        conn = pymongo.MongoClient(self.cnx_string, **options)
        # authentication doesn't actually happen until we interact with a database
        conn["admin"]["myCol"].insert_one({})

    def assert_x509_authentication(self, cert_file_name: str, attempts: int = 5):
        assert attempts > 0
        options = {
            "ssl": True,
            "authMechanism": "MONGODB-X509",
            "ssl_certfile": cert_file_name,
            "ssl_cert_reqs": ssl.CERT_REQUIRED,
            "ssl_ca_certs": "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
        }

        total_attempts = attempts
        while True:
            attempts -= 1
            try:
                pymongo.MongoClient(self.cnx_string, **options)
                return
            except OperationFailure:
                if attempts == 0:
                    fail(msg=f"unable to authenticate after {total_attempts} attempts")
                time.sleep(5)

    def upload_random_data(self, count, test_collection=TEST_COLLECTION):
        """ Generates random json documents and uploads them to database. This data can be later checked for
        integrity """
        print(
            "Inserting {} fake records to {}.{}".format(count, TEST_DB, test_collection)
        )
        target = self.client[TEST_DB][test_collection]
        buf = []
        for a in range(count):
            buf.append(generate_single_json())
            if len(buf) == 10_000:
                target.insert_many(buf)
                buf.clear()
            if (a + 1) % 50_000 == 0:
                print("Inserted {} records".format(a + 1))
        # tail
        if len(buf) > 0:
            target.insert_many(buf)
        print("Data inserted")


class StandaloneTester(MongoTester):
    def __init__(self, mdb_resource_name: str, ssl: bool = False, srv: bool = False):
        self.cnx_string = build_mongodb_connection_uri(
            mdb_resource_name, KubernetesTester.get_namespace(), 1
        )
        super().__init__(self.cnx_string, ssl)


class ReplicaSetTester(MongoTester):
    def __init__(
        self,
        mdb_resource_name: str,
        replicas_count: int,
        ssl: bool = False,
        srv: bool = False,
    ):
        self.replicas_count = replicas_count

        self.cnx_string = build_mongodb_connection_uri(
            mdb_resource_name,
            KubernetesTester.get_namespace(),
            replicas_count,
            servicename=None,
            srv=srv,
        )

        super().__init__(self.cnx_string, ssl)

    def assert_connectivity(
        self, wait_for=60, check_every=5, with_srv=False, attempts: int = 5
    ):
        """ For replica sets in addition to is_master() we need to make sure all replicas are up """
        super().assert_connectivity(attempts=attempts)

        check_times = wait_for // check_every

        while (
            self.client.primary is None
            or len(self.client.secondaries) < self.replicas_count - 1
        ) and check_times >= 0:
            time.sleep(check_every)
            check_times -= 1

        assert self.client.primary is not None
        assert len(self.client.secondaries) == self.replicas_count - 1


class ShardedClusterTester(MongoTester):
    def __init__(
        self,
        mdb_resource_name: str,
        mongos_count: int,
        ssl: bool = False,
        srv: bool = False,
    ):
        mdb_name = mdb_resource_name + "-mongos"
        servicename = mdb_resource_name + "-svc"

        self.cnx_string = build_mongodb_connection_uri(
            mdb_name, KubernetesTester.get_namespace(), mongos_count, servicename
        )
        super().__init__(self.cnx_string, ssl)

    def shard_collection(
        self, shards_pattern, shards_count, key, test_collection=TEST_COLLECTION
    ):
        """ enables sharding and creates zones to make sure data is spread over shards.
        Assumes that the documents have field 'key' with value in [0,10] range """
        for i in range(shards_count):
            self.client.admin.command(
                "addShardToZone", shards_pattern.format(i), zone="zone-{}".format(i)
            )

        for i in range(shards_count):
            self.client.admin.command(
                "updateZoneKeyRange",
                db_namespace(test_collection),
                min={key: i * (10 / shards_count)},
                max={key: (i + 1) * (10 / shards_count)},
                zone="zone-{}".format(i),
            )

        self.client.admin.command("enableSharding", TEST_DB)
        self.client.admin.command(
            "shardCollection", db_namespace(test_collection), key={key: 1}
        )

    def prepare_for_shard_removal(self, shards_pattern, shards_count):
        """ We need to map all the shards to all the zones to let shard be removed (otherwise the balancer gets
        stuck as it cannot move chunks from shards being removed) """
        for i in range(shards_count):
            for j in range(shards_count):
                self.client.admin.command(
                    "addShardToZone", shards_pattern.format(i), zone="zone-{}".format(j)
                )

    def assert_number_of_shards(self, expected_count):
        assert len(self.client.admin.command("listShards")["shards"]) == expected_count


# ------------------------- Helper functions ----------------------------


def build_mongodb_connection_uri(
    mdb_resource: str,
    namespace: str,
    members: int,
    servicename: str = None,
    srv: bool = False,
) -> str:
    if servicename is None:
        servicename = "{}-svc".format(mdb_resource)

    if srv:
        return build_mongodb_uri(build_host_srv(servicename, namespace), srv)
    else:
        return build_mongodb_uri(
            build_list_of_hosts(mdb_resource, namespace, members, servicename)
        )


def build_list_of_hosts(
    mdb_resource: str, namespace: str, members: int, servicename: str
) -> List[str]:
    return [
        build_host_fqdn("{}-{}".format(mdb_resource, idx), namespace, servicename)
        for idx in range(members)
    ]


def build_host_fqdn(hostname: str, namespace: str, servicename: str) -> str:
    return "{hostname}.{servicename}.{namespace}.svc.cluster.local:27017".format(
        hostname=hostname, servicename=servicename, namespace=namespace
    )


def build_host_srv(servicename: str, namespace: str) -> str:
    srv_host = "{servicename}.{namespace}.svc.cluster.local".format(
        servicename=servicename, namespace=namespace
    )
    return srv_host


def build_mongodb_uri(hosts, srv: bool = False) -> str:
    plus_srv = ""
    if srv:
        plus_srv = "+srv"
    else:
        hosts = ",".join(hosts)

    return "mongodb{}://{}".format(plus_srv, hosts)


def generate_single_json():
    """ Generates a json with two fields. String field contains random characters and has length 100 characters. """
    random_str = "".join([random.choice(string.ascii_lowercase) for _ in range(100)])
    return {"description": random_str, "type": random.uniform(1, 10)}


def db_namespace(collection):
    """ https://docs.mongodb.com/manual/reference/glossary/#term-namespace """
    return "{}.{}".format(TEST_DB, collection)
