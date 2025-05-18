import copy
import inspect
import logging
import os
import random
import string
import threading
import time
from typing import Callable, Dict, List, Optional

import pymongo
from kubetester.kubetester import KubernetesTester
from opentelemetry import trace
from pycognito import Cognito
from pymongo.auth_oidc import OIDCCallback, OIDCCallbackContext, OIDCCallbackResult
from pymongo.errors import OperationFailure, PyMongoError, ServerSelectionTimeoutError
from pytest import fail

from kubetester import kubetester

TEST_DB = "test-db"
TEST_COLLECTION = "test-collection"


def with_tls(use_tls: bool = False, ca_path: Optional[str] = None) -> Dict[str, str]:
    # SSL is set to true by default if using mongodb+srv, it needs to be explicitely set to false
    # https://docs.mongodb.com/manual/reference/program/mongo/index.html#cmdoption-mongo-host
    options = {"tls": use_tls}

    if use_tls:
        options["tlsCAFile"] = kubetester.SSL_CA_CERT if ca_path is None else ca_path
    return options


def with_scram(username: str, password: str, auth_mechanism: str = "SCRAM-SHA-256") -> Dict[str, str]:
    valid_mechanisms = {"SCRAM-SHA-256", "SCRAM-SHA-1"}
    if auth_mechanism not in valid_mechanisms:
        raise ValueError(f"auth_mechanism must be one of {valid_mechanisms}, but was {auth_mechanism}.")

    return {
        "authMechanism": auth_mechanism,
        "password": password,
        "username": username,
    }


def with_x509(cert_file_name: str, ca_path: Optional[str] = None) -> Dict[str, str]:
    options = with_tls(True, ca_path=ca_path)
    options.update(
        {
            "authMechanism": "MONGODB-X509",
            "tlsCertificateKeyFile": cert_file_name,
            "tlsAllowInvalidCertificates": False,
        }
    )
    return options


def with_ldap(ssl_certfile: Optional[str] = None, tls_ca_file: Optional[str] = None) -> Dict[str, str]:
    options = {}
    if tls_ca_file is not None:
        options.update(with_tls(True, tls_ca_file))
    if ssl_certfile is not None:
        options["tlsCertificateKeyFile"] = ssl_certfile
    return options


class MyOIDCCallback(OIDCCallback):
    def fetch(self, context: OIDCCallbackContext) -> OIDCCallbackResult:
        u = Cognito(
            user_pool_id=os.getenv("cognito_user_pool_id"),
            client_id=os.getenv("cognito_workload_federation_client_id"),
            username=os.getenv("cognito_user_name"),
            client_secret=os.getenv("cognito_workload_federation_client_secret"),
        )
        u.authenticate(password=os.getenv("cognito_user_password"))
        return OIDCCallbackResult(access_token=u.id_token)


class MongoTester:
    """MongoTester is a general abstraction to work with mongo database. It encapsulates the client created in
    the constructor. All general methods non-specific to types of mongodb topologies should reside here."""

    def __init__(
        self,
        connection_string: str,
        use_ssl: bool,
        ca_path: Optional[str] = None,
    ):
        self.default_opts = with_tls(use_ssl, ca_path)
        self.default_opts["serverSelectionTimeoutMs"] = "120000"  # 2 minutes
        self.cnx_string = connection_string
        self.client = None
        logging.info(
            f"Initialized MongoTester with connection string: {connection_string}, TLS: {use_ssl} and CA Path: {ca_path}"
        )

    @property
    def client(self):
        if self._client is None:
            self._client = self._init_client(**self.default_opts)
        return self._client

    @client.setter
    def client(self, value):
        self._client = value

    def _merge_options(self, opts: List[Dict[str, str]]) -> Dict[str, str]:
        options = copy.deepcopy(self.default_opts)
        for opt in opts:
            options.update(opt)
        return options

    def _init_client(self, **kwargs):
        return pymongo.MongoClient(self.cnx_string, **kwargs)

    def assert_connectivity(
        self,
        attempts: int = 20,
        db: str = "admin",
        col: str = "myCol",
        opts: Optional[List[Dict[str, any]]] = None,
        write_concern: pymongo.WriteConcern = None,
    ):
        if opts is None:
            opts = []

        options = self._merge_options(opts)
        self.client = self._init_client(**options)

        assert attempts > 0
        while True:
            attempts -= 1
            try:
                logging.warning(f"connected nodes: {self.client.nodes}")
                self.client.admin.command("ismaster")
                if write_concern:
                    d = self.client.get_database(name=db, write_concern=write_concern)
                    c = d.get_collection(name=col)
                    c.insert_one({})
                if "authMechanism" in options:
                    # Perform an action that will require auth.
                    self.client[db][col].insert_one({})
            except PyMongoError:
                if attempts == 0:
                    raise
                time.sleep(5)
            else:
                break

    def assert_no_connection(self, opts: Optional[List[Dict[str, str]]] = None):
        try:
            if opts:
                opts.append({"serverSelectionTimeoutMs": "30000"})
            else:
                opts = [{"serverSelectionTimeoutMs": "30000"}]
            self.assert_connectivity(opts=opts)
            fail()
        except ServerSelectionTimeoutError:
            pass

    def assert_version(self, expected_version: str):
        # version field does not contain -ent suffix in MongoDB
        assert self.client.admin.command("buildInfo")["version"] == expected_version.split("-")[0]
        if expected_version.endswith("-ent"):
            self.assert_is_enterprise()

    def assert_data_size(self, expected_count, test_collection=TEST_COLLECTION):
        assert self.client[TEST_DB][test_collection].estimated_document_count() == expected_count

    def assert_is_enterprise(self):
        assert "enterprise" in self.client.admin.command("buildInfo")["modules"]

    def assert_scram_sha_authentication(
        self,
        username: str,
        password: str,
        auth_mechanism: str,
        attempts: int = 20,
        ssl: bool = False,
        **kwargs,
    ) -> None:
        assert attempts > 0
        assert auth_mechanism in {"SCRAM-SHA-256", "SCRAM-SHA-1"}

        for i in reversed(range(attempts)):
            try:
                self._authenticate_with_scram(
                    username,
                    password,
                    auth_mechanism=auth_mechanism,
                    ssl=ssl,
                    **kwargs,
                )
                return
            except OperationFailure as e:
                if i == 0:
                    fail(msg=f"unable to authenticate after {attempts} attempts with error: {e}")
                time.sleep(5)

    def assert_scram_sha_authentication_fails(
        self,
        username: str,
        password: str,
        retries: int = 20,
        ssl: bool = False,
        **kwargs,
    ):
        """
        If a password has changed, it could take some time for the user changes to propagate, meaning
        this could return true if we make a CRD change and immediately try to auth as the old user
        which still exists. When we change a password, we should eventually no longer be able to auth with
        that user's credentials.
        """
        for i in range(retries):
            try:
                self._authenticate_with_scram(username, password, ssl=ssl, **kwargs)
            except OperationFailure:
                return
            time.sleep(5)
        fail(f"was still able to authenticate with username={username} password={password} after {retries} attempts")

    def _authenticate_with_scram(
        self,
        username: str,
        password: str,
        auth_mechanism: str,
        ssl: bool = False,
        **kwargs,
    ):

        options = self._merge_options(
            [
                with_tls(ssl, ca_path=kwargs.get("tlsCAFile")),
                with_scram(username, password, auth_mechanism),
            ]
        )

        self.client = self._init_client(**options)
        # authentication doesn't actually happen until we interact with a database
        self.client["admin"]["myCol"].insert_one({})

    def assert_x509_authentication(self, cert_file_name: str, attempts: int = 20, **kwargs):
        assert attempts > 0

        options = self._merge_options(
            [
                with_x509(cert_file_name, kwargs.get("tlsCAFile", kubetester.SSL_CA_CERT)),
            ]
        )

        total_attempts = attempts
        while True:
            attempts -= 1
            try:
                self.client = self._init_client(**options)
                self.client["admin"]["myCol"].insert_one({})
                return
            except OperationFailure:
                if attempts == 0:
                    fail(msg=f"unable to authenticate after {total_attempts} attempts")
                time.sleep(5)

    def assert_ldap_authentication(
        self,
        username: str,
        password: str,
        db: str = "admin",
        collection: str = "myCol",
        tls_ca_file: Optional[str] = None,
        ssl_certfile: str = None,
        attempts: int = 20,
    ):

        options = with_ldap(ssl_certfile, tls_ca_file)
        total_attempts = attempts

        while True:
            attempts -= 1
            try:
                client = self._init_client(
                    **options,
                    username=username,
                    password=password,
                    authSource="$external",
                    authMechanism="PLAIN",
                )
                client[db][collection].insert_one({"data": "I need to exist!"})

                return
            except OperationFailure:
                if attempts <= 0:
                    fail(msg=f"unable to authenticate after {total_attempts} attempts")
                time.sleep(5)

    def assert_oidc_authentication(
        self,
        db: str = "admin",
        collection: str = "myCol",
        attempts: int = 10,
    ):
        assert attempts > 0

        props = {"OIDC_CALLBACK": MyOIDCCallback()}

        total_attempts = attempts
        while True:
            attempts -= 1
            try:
                # Initialize the MongoDB client with OIDC authentication
                self.client = self._init_client(
                    authMechanism="MONGODB-OIDC",
                    authMechanismProperties=props,
                )
                # Perform a write operation to test authentication
                self.client[db][collection].insert_one({"test": "oidc_auth_test"})
                return
            except OperationFailure as e:
                if attempts == 0:
                    raise RuntimeError(f"Unable to authenticate after {total_attempts} attempts: {e}")
                time.sleep(5)

    def assert_oidc_authentication_fails(
        self,
        db: str = "admin",
        collection: str = "myCol",
        attempts: int = 10
    ):
        assert attempts > 0
        total_attempts = attempts
        while True:
            attempts -= 1
            try:
                if attempts <= 0:
                    fail(msg=f"was able to authenticate with OIDC after {total_attempts} attempts")

                self.assert_oidc_authentication(db, collection, 1)
                time.sleep(5)
            except RuntimeError:
                return


    def upload_random_data(
        self,
        count: int,
        generation_function: Optional[Callable] = None,
    ):
        return upload_random_data(self.client, count, generation_function)

    def assert_deployment_reachable(self, attempts: int = 10):
        """See: https://jira.mongodb.org/browse/CLOUDP-68873
        the agents might report being in goal state, the MDB resource
        would report no errors but the deployment would be unreachable
        The workaround is to use the public API to get the list of
        hosts and check the typeName field of each host.
        This would be NO_DATA if the hosts are not reachable
        See docs: https://docs.opsmanager.mongodb.com/current/reference/api/hosts/get-all-hosts-in-group/#response-document
        at the "typeName" field
        """
        while True:
            hosts_unreachable = 0
            attempts -= 1
            hosts = KubernetesTester.get_hosts()
            print(f"hosts: {hosts}")
            for host in hosts["results"]:
                print(f"current host: {host}")
                if host["typeName"] == "NO_DATA":
                    hosts_unreachable += 1
            if hosts_unreachable == 0:
                return
            if attempts <= 0:
                fail(msg="Some hosts still report NO_DATA state")
            time.sleep(10)


class StandaloneTester(MongoTester):
    def __init__(
        self,
        mdb_resource_name: str,
        ssl: bool = False,
        ca_path: Optional[str] = None,
        namespace: Optional[str] = None,
        port="27017",
        external_domain: Optional[str] = None,
        cluster_domain: str = "cluster.local",
    ):
        if namespace is None:
            namespace = KubernetesTester.get_namespace()

        self.cnx_string = build_mongodb_connection_uri(
            mdb_resource_name, namespace, 1, port, external_domain=external_domain, cluster_domain=cluster_domain
        )
        super().__init__(self.cnx_string, ssl, ca_path)


class ReplicaSetTester(MongoTester):
    def __init__(
        self,
        mdb_resource_name: str,
        replicas_count: int,
        ssl: bool = False,
        srv: bool = False,
        ca_path: Optional[str] = None,
        namespace: Optional[str] = None,
        port="27017",
        external_domain: Optional[str] = None,
        cluster_domain: str = "cluster.local",
    ):
        if namespace is None:
            # backward compatibility with docstring tests
            namespace = KubernetesTester.get_namespace()

        self.replicas_count = replicas_count

        self.cnx_string = build_mongodb_connection_uri(
            mdb_resource_name,
            namespace,
            replicas_count,
            servicename=None,
            srv=srv,
            port=port,
            external_domain=external_domain,
            cluster_domain=cluster_domain,
        )

        super().__init__(self.cnx_string, ssl, ca_path)

    def assert_connectivity(
        self,
        wait_for=60,
        check_every=5,
        with_srv=False,
        attempts: int = 5,
        write_concern: pymongo.WriteConcern = None,
        opts: Optional[List[Dict[str, str]]] = None,
    ):
        """For replica sets in addition to is_master() we need to make sure all replicas are up"""
        super().assert_connectivity(attempts=attempts, write_concern=write_concern, opts=opts)

        if self.replicas_count == 1:
            # On 1 member replica-set, the last member is considered primary and secondaries will be `set()`
            assert self.client.is_primary
            assert len(self.client.secondaries) == 0
            return

        check_times = wait_for // check_every

        while (
            self.client.primary is None or len(self.client.secondaries) < self.replicas_count - 1
        ) and check_times >= 0:
            time.sleep(check_every)
            check_times -= 1

        assert self.client.primary is not None
        assert len(self.client.secondaries) == self.replicas_count - 1


class MultiReplicaSetTester(MongoTester):
    def __init__(
        self,
        service_names: List[str],
        port: str,
        ssl: Optional[bool] = False,
        ca_path: Optional[str] = None,
        namespace: Optional[str] = None,
        external: bool = False,
    ):
        super().__init__(
            build_mongodb_multi_connection_uri(namespace, service_names, port, external=external),
            use_ssl=ssl,
            ca_path=ca_path,
        )


class ShardedClusterTester(MongoTester):
    def __init__(
        self,
        mdb_resource_name: str,
        mongos_count: int,
        ssl: bool = False,
        srv: bool = False,
        ca_path: Optional[str] = None,
        namespace: Optional[str] = None,
        port="27017",
        cluster_domain: str = "cluster.local",
        multi_cluster: Optional[bool] = False,
        service_names: Optional[list[str]] = None,
        external_domain: str = None,
    ):
        mdb_name = mdb_resource_name + "-mongos"
        servicename = mdb_resource_name + "-svc"

        if namespace is None:
            # backward compatibility with docstring tests
            namespace = KubernetesTester.get_namespace()

        if multi_cluster:
            self.cnx_string = build_mongodb_multi_connection_uri(
                namespace,
                service_names=service_names,
                port=port,
                cluster_domain=cluster_domain,
                external=external_domain is not None,
            )
        else:
            self.cnx_string = build_mongodb_connection_uri(
                mdb_name,
                namespace,
                mongos_count,
                port=port,
                servicename=servicename,
                srv=srv,
                cluster_domain=cluster_domain,
                external_domain=external_domain,
            )
        super().__init__(self.cnx_string, ssl, ca_path)

    def shard_collection(self, shards_pattern, shards_count, key, test_collection=TEST_COLLECTION):
        """enables sharding and creates zones to make sure data is spread over shards.
        Assumes that the documents have field 'key' with value in [0,10] range"""
        for i in range(shards_count):
            self.client.admin.command("addShardToZone", shards_pattern.format(i), zone="zone-{}".format(i))

        for i in range(shards_count):
            self.client.admin.command(
                "updateZoneKeyRange",
                db_namespace(test_collection),
                min={key: i * (10 / shards_count)},
                max={key: (i + 1) * (10 / shards_count)},
                zone="zone-{}".format(i),
            )

        self.client.admin.command("enableSharding", TEST_DB)
        self.client.admin.command("shardCollection", db_namespace(test_collection), key={key: 1})

    def prepare_for_shard_removal(self, shards_pattern, shards_count):
        """We need to map all the shards to all the zones to let shard be removed (otherwise the balancer gets
        stuck as it cannot move chunks from shards being removed)"""
        for i in range(shards_count):
            for j in range(shards_count):
                self.client.admin.command("addShardToZone", shards_pattern.format(i), zone="zone-{}".format(j))

    def assert_number_of_shards(self, expected_count):
        assert len(self.client.admin.command("listShards")["shards"]) == expected_count


class BackgroundHealthChecker(threading.Thread):
    """BackgroundHealthChecker is the thread which periodically calls the function to check health of some resource. It's
    run as a daemon, so usually there's no need to stop it manually.
    """

    def __init__(
        self,
        health_function,
        wait_sec: int = 3,
        allowed_sequential_failures: int = 3,
        health_function_params=None,
    ):
        super().__init__()
        if health_function_params is None:
            health_function_params = {}
        self._stop_event = threading.Event()
        self.health_function = health_function
        self.health_function_params = health_function_params
        self.wait_sec = wait_sec
        self.allowed_sequential_failures = allowed_sequential_failures
        self.exception_number = 0
        self.last_exception = None
        self.daemon = True
        self.max_consecutive_failure = 0
        self.number_of_runs = 0

    def run(self):
        consecutive_failure = 0
        while not self._stop_event.isSet():
            self.number_of_runs += 1
            try:
                self.health_function(**self.health_function_params)
                consecutive_failure = 0
            except Exception as e:
                print(f"Error in {self.__class__.__name__}: {e})")
                self.last_exception = e
                consecutive_failure = consecutive_failure + 1
                self.max_consecutive_failure = max(self.max_consecutive_failure, consecutive_failure)
                self.exception_number = self.exception_number + 1
            time.sleep(self.wait_sec)

    def stop(self):
        self._stop_event.set()

    def assert_healthiness(self, allowed_rate_of_failure: Optional[float] = None):
        """

        `allowed_rate_of_failure` allows you to define a rate of allowed failures,
        instead of the default, absolute amount of failures.

        `allowed_rate_of_failure` is a number between 0 and 1 that desribes a "percentage"
        of tolerated failures.

        For instance, the following values:

        - 0.1 -- means that 10% of the requests might fail, before
                 failing the tests.
        - 0.9 -- 90% of checks are allowed to fail.
        - 0.0 -- very strict: no checks are allowed to fail.
        - 1.0 -- very relaxed: all checks can fail.

        """
        print("\nlongest consecutive failures: {}".format(self.max_consecutive_failure))
        print("total exceptions count: {}".format(self.exception_number))
        print("total checks number: {}".format(self.number_of_runs))

        allowed_failures = self.allowed_sequential_failures
        if allowed_rate_of_failure is not None:
            allowed_failures = self.number_of_runs * allowed_rate_of_failure

        # Automatically get the caller file information
        caller_info = inspect.stack()[1]  # Stack frame of the caller
        caller_file = caller_info.filename  # Get the filename of the caller
        caller_function = caller_info.function  # Optional: Get the function name

        span = trace.get_current_span()
        span.set_attribute("mck.version_change_connection_failures", allowed_failures)
        span.set_attribute("mck.caller_file", caller_file)
        span.set_attribute("mck.caller_function", caller_function)
        span.set_attribute("mck.health_check_failed", True)

        try:
            assert self.max_consecutive_failure <= allowed_failures
            assert self.number_of_runs > 0
        except AssertionError as e:
            span.set_attribute("mck.health_check_failed", True)
            span.set_attribute("mck.failure_reason", str(e))
            raise


class MongoDBBackgroundTester(BackgroundHealthChecker):
    def __init__(
        self,
        mongo_tester: MongoTester,
        wait_sec: int = 3,
        allowed_sequential_failures: int = 1,
        health_function_params=None,
    ):
        if health_function_params is None:
            health_function_params = {"attempts": 1}
        super().__init__(
            health_function=mongo_tester.assert_connectivity,
            wait_sec=wait_sec,
            health_function_params=health_function_params,
            allowed_sequential_failures=allowed_sequential_failures,
        )


def build_mongodb_connection_uri(
    mdb_resource: str,
    namespace: str,
    members: int,
    port: str,
    servicename: str = None,
    srv: bool = False,
    external_domain: str = None,
    cluster_domain: str = "cluster.local",
) -> str:
    if servicename is None:
        servicename = "{}-svc".format(mdb_resource)

    if external_domain:
        return build_mongodb_uri(build_list_of_hosts_with_external_domain(mdb_resource, members, external_domain, port))
    if srv:
        return build_mongodb_uri(build_host_srv(servicename, namespace, cluster_domain), srv)
    else:
        return build_mongodb_uri(
            build_list_of_hosts(mdb_resource, namespace, members, servicename, port, cluster_domain)
        )


def build_mongodb_multi_connection_uri(
    namespace: str,
    service_names: List[str],
    port: str,
    external: bool = False,
    cluster_domain: str = "cluster.local",
) -> str:
    return build_mongodb_uri(
        build_list_of_multi_hosts(namespace, service_names, port, external=external, cluster_domain=cluster_domain)
    )


def build_list_of_hosts(
    mdb_resource: str, namespace: str, members: int, servicename: str, port: str, cluster_domain: str
) -> List[str]:
    return [
        build_host_fqdn("{}-{}".format(mdb_resource, idx), namespace, servicename, port, cluster_domain)
        for idx in range(members)
    ]


def build_list_of_hosts_with_external_domain(
    mdb_resource: str, members: int, external_domain: str, port: str
) -> List[str]:
    return [f"{mdb_resource}-{idx}.{external_domain}:{port}" for idx in range(members)]


def build_list_of_multi_hosts(
    namespace: str, service_names: List[str], port, external: bool = False, cluster_domain: str = "cluster.local"
) -> List[str]:
    if external:
        return [f"{service_name}:{port}" for service_name in service_names]
    return [
        build_host_service_fqdn(service_name, namespace, port, cluster_domain=cluster_domain)
        for service_name in service_names
    ]


def build_host_service_fqdn(servicename: str, namespace: str, port: int, cluster_domain: str = "cluster.local") -> str:
    return f"{servicename}.{namespace}.svc.{cluster_domain}:{port}"


def build_host_fqdn(hostname: str, namespace: str, servicename: str, port, cluster_domain: str) -> str:
    return "{hostname}.{servicename}.{namespace}.svc.{cluster_domain}:{port}".format(
        hostname=hostname, servicename=servicename, namespace=namespace, port=port, cluster_domain=cluster_domain
    )


def build_host_srv(servicename: str, namespace: str, cluster_domain: str) -> str:
    srv_host = "{servicename}.{namespace}.svc.{cluster_domain}".format(
        servicename=servicename, namespace=namespace, cluster_domain=cluster_domain
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
    """Generates a json with two fields. String field contains random characters and has length 100 characters."""
    random_str = "".join([random.choice(string.ascii_lowercase) for _ in range(100)])
    return {"description": random_str, "type": random.uniform(1, 10)}


def db_namespace(collection):
    """https://docs.mongodb.com/manual/reference/glossary/#term-namespace"""
    return "{}.{}".format(TEST_DB, collection)


def upload_random_data(
    client,
    count: int,
    generation_function: Optional[Callable] = None,
    task_name: Optional[str] = "default",
):
    """
    Generates random json documents and uploads them to database. This data can
    be later checked for integrity.
    """

    if generation_function is None:
        generation_function = generate_single_json

    logging.info("task: {}. Inserting {} fake records to {}.{}".format(task_name, count, TEST_DB, TEST_COLLECTION))

    target = client[TEST_DB][TEST_COLLECTION]
    buf = []

    for a in range(count):
        buf.append(generation_function())
        if len(buf) == 1_000:
            target.insert_many(buf)
            buf.clear()
        if (a + 1) % 10_000 == 0:
            logging.info("task: {}. Inserted {} document".format(task_name, a + 1))
    # tail
    if len(buf) > 0:
        target.insert_many(buf)

    logging.info("task: {}. Task finished, {} documents inserted. ".format(task_name, count))
