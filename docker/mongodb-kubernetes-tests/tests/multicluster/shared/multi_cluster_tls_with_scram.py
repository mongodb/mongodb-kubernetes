from typing import List

import kubernetes
from kubetester import create_secret, read_secret
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import with_scram, with_tls
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase

USER_NAME = "my-user-1"
USER_RESOURCE = "multi-replica-set-scram-user"
USER_DATABASE = "admin"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_deploy_mongodb_multi_with_tls(
    mongodb_multi: MongoDBMulti | MongoDB,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


def test_update_mongodb_multi_tls_with_scram(
    mongodb_multi: MongoDBMulti | MongoDB,
    namespace: str,
):
    mongodb_multi.load()
    mongodb_multi["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


def test_create_mongodb_user(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_user: MongoDBUser,
    namespace: str,
):
    # create user secret first
    create_secret(
        namespace=namespace,
        name=PASSWORD_SECRET_NAME,
        data={"password": USER_PASSWORD},
        api_client=central_cluster_client,
    )
    mongodb_user.assert_reaches_phase(Phase.Updated, timeout=100)


def test_tls_connectivity(mongodb_multi: MongoDBMulti | MongoDB, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])


def test_replica_set_connectivity_with_scram_and_tls(mongodb_multi: MongoDBMulti | MongoDB, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(
        db="admin",
        opts=[
            with_scram(USER_NAME, USER_PASSWORD),
            with_tls(use_tls=True, ca_path=ca_path),
        ],
    )


def test_replica_set_connectivity_from_connection_string_standard(
    namespace: str,
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
    ca_path: str,
):
    secret_data = read_secret(
        namespace,
        f"{mongodb_multi.name}-{USER_RESOURCE}-{USER_DATABASE}",
        api_client=member_cluster_clients[0].api_client,
    )
    tester = mongodb_multi.tester()
    tester.cnx_string = secret_data["connectionString.standard"]
    tester.assert_connectivity(
        db="admin",
        opts=[
            with_scram(USER_NAME, USER_PASSWORD),
            with_tls(use_tls=True, ca_path=ca_path),
        ],
    )


def test_replica_set_connectivity_from_connection_string_standard_srv(
    namespace: str,
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
    ca_path: str,
):
    secret_data = read_secret(
        namespace,
        f"{mongodb_multi.name}-{USER_RESOURCE}-{USER_DATABASE}",
        api_client=member_cluster_clients[-1].api_client,
    )
    tester = mongodb_multi.tester()
    tester.cnx_string = secret_data["connectionString.standardSrv"]
    tester.assert_connectivity(
        db="admin",
        opts=[
            with_scram(USER_NAME, USER_PASSWORD),
            with_tls(use_tls=True, ca_path=ca_path),
        ],
    )


def test_mongodb_multi_tls_enable_x509(
    mongodb_multi: MongoDBMulti | MongoDB,
    namespace: str,
):
    mongodb_multi.load()

    mongodb_multi["spec"]["security"]["authentication"]["modes"].append("X509")
    mongodb_multi["spec"]["security"]["authentication"]["agents"] = {"mode": "SCRAM"}
    mongodb_multi.update()

    # sometimes the agents need more time to register than the time we wait ->
    # "Failed to enable Authentication for MongoDB Multi Replicaset"
    # after this the agents eventually succeed.
    mongodb_multi.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=1000)


def test_mongodb_multi_tls_automation_config_was_updated(
    mongodb_multi: MongoDBMulti | MongoDB,
    namespace: str,
):
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    tester.assert_authentication_mechanism_enabled("MONGODB-X509", active_auth_mechanism=False)
    tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
    tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=2)
