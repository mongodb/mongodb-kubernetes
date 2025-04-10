from typing import Dict, Optional

import pytest
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import (
    create_configmap,
    create_secret,
    delete_secret,
    get_default_storage_class,
    get_statefulset,
    random_k8s_name,
    read_secret,
)
from kubetester.awss3client import AwsS3Client, s3_endpoint
from kubetester.certs import create_mongodb_tls_certs, create_ops_manager_tls_certs
from kubetester.http import https_endpoint_is_reachable
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase, get_pods
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark

from . import assert_secret_in_vault, run_command_in_vault, store_secret_in_vault

OPERATOR_NAME = "mongodb-enterprise-operator"
APPDB_SA_NAME = "mongodb-enterprise-appdb"
OM_SA_NAME = "mongodb-enterprise-ops-manager"
OM_NAME = "om-basic"
S3_RS_NAME = "my-mongodb-s3"
S3_SECRET_NAME = "my-s3-secret"
OPLOG_RS_NAME = "my-mongodb-oplog"

DATABASE_SA_NAME = "mongodb-enterprise-database-pods"


def certs_for_prometheus(issuer: str, namespace: str, resource_name: str) -> str:
    secret_name = random_k8s_name(resource_name + "-") + "-prometheus-cert"

    return create_mongodb_tls_certs(
        issuer,
        namespace,
        resource_name,
        secret_name,
        secret_backend="Vault",
        vault_subpath="appdb",
    )


def new_om_s3_store(
    mdb: MongoDB,
    s3_id: str,
    s3_bucket_name: str,
    aws_s3_client: AwsS3Client,
    assignment_enabled: bool = True,
    path_style_access_enabled: bool = True,
    user_name: Optional[str] = None,
    password: Optional[str] = None,
) -> Dict:
    return {
        "uri": mdb.mongo_uri(user_name=user_name, password=password),
        "id": s3_id,
        "pathStyleAccessEnabled": path_style_access_enabled,
        "s3BucketEndpoint": s3_endpoint(AWS_REGION),
        "s3BucketName": s3_bucket_name,
        "awsAccessKey": aws_s3_client.aws_access_key,
        "awsSecretKey": aws_s3_client.aws_secret_access_key,
        "assignmentEnabled": assignment_enabled,
    }


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client, namespace: str, vault_namespace: str, vault_name: str) -> str:
    create_aws_secret(aws_s3_client, S3_SECRET_NAME, vault_namespace, vault_name, namespace)
    yield from create_s3_bucket(aws_s3_client, bucket_prefix="test-s3-bucket-")


def create_aws_secret(
    aws_s3_client,
    secret_name: str,
    vault_namespace: str,
    vault_name: str,
    namespace: str,
):
    data = {
        "accessKey": aws_s3_client.aws_access_key,
        "secretKey": aws_s3_client.aws_secret_access_key,
    }
    path = f"secret/mongodbenterprise/operator/{namespace}/{secret_name}"
    store_secret_in_vault(vault_namespace, vault_name, data, path)


def create_s3_bucket(aws_s3_client, bucket_prefix: str = "test-bucket-"):
    """creates a s3 bucket and a s3 config"""
    bucket_prefix = KubernetesTester.random_k8s_name(bucket_prefix)
    aws_s3_client.create_s3_bucket(bucket_prefix)
    print("Created S3 bucket", bucket_prefix)

    yield bucket_prefix
    print("\nRemoving S3 bucket", bucket_prefix)
    aws_s3_client.delete_s3_bucket(bucket_prefix)


@fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    s3_bucket: str,
    issuer_ca_configmap: str,
    issuer: str,
    vault_namespace: str,
    vault_name: str,
) -> MongoDBOpsManager:
    om = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace)

    om["spec"]["backup"] = {
        "enabled": True,
        "s3Stores": [
            {
                "name": "s3Store1",
                "s3BucketName": s3_bucket,
                "mongodbResourceRef": {"name": "my-mongodb-s3"},
                "s3SecretRef": {"name": S3_SECRET_NAME},
                "pathStyleAccessEnabled": True,
                "s3BucketEndpoint": "s3.us-east-1.amazonaws.com",
            },
        ],
        "headDB": {
            "storage": "500M",
            "storageClass": get_default_storage_class(),
        },
        "opLogStores": [
            {
                "name": "oplog1",
                "mongodbResourceRef": {"name": "my-mongodb-oplog"},
            },
        ],
    }
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)

    prom_cert_secret = certs_for_prometheus(issuer, namespace, om.name + "-db")
    store_secret_in_vault(
        vault_namespace,
        vault_name,
        {"password": "prom-password"},
        f"secret/mongodbenterprise/operator/{namespace}/prom-password",
    )

    om["spec"]["applicationDatabase"]["prometheus"] = {
        "username": "prom-user",
        "passwordSecretRef": {"name": "prom-password"},
        "tlsSecretKeyRef": {
            "name": prom_cert_secret,
        },
    }

    return om.create()


@fixture(scope="module")
def oplog_replica_set(ops_manager, namespace, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=OPLOG_RS_NAME,
    ).configure(ops_manager, "development")

    resource.set_version(custom_mdb_version)

    yield resource.create()


@fixture(scope="module")
def s3_replica_set(ops_manager, namespace, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=S3_RS_NAME,
    ).configure(ops_manager, "s3metadata")

    resource.set_version(custom_mdb_version)
    yield resource.create()


@mark.e2e_vault_setup_om_backup
def test_vault_creation(vault: str, vault_name: str, vault_namespace: str):
    vault
    sts = get_statefulset(namespace=vault_namespace, name=vault_name)
    assert sts.status.ready_replicas == 1


@mark.e2e_vault_setup_om_backup
def test_create_vault_operator_policy(vault_name: str, vault_namespace: str):
    # copy hcl file from local machine to pod
    KubernetesTester.copy_file_inside_pod(
        f"{vault_name}-0",
        "vaultpolicies/operator-policy.hcl",
        "/tmp/operator-policy.hcl",
        namespace=vault_namespace,
    )

    cmd = [
        "vault",
        "policy",
        "write",
        "mongodbenterprise",
        "/tmp/operator-policy.hcl",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om_backup
def test_enable_kubernetes_auth(vault_name: str, vault_namespace: str):
    cmd = [
        "vault",
        "auth",
        "enable",
        "kubernetes",
    ]

    run_command_in_vault(
        vault_namespace,
        vault_name,
        cmd,
        expected_message=["Success!", "path is already in use at kubernetes"],
    )

    cmd = [
        "cat",
        "/var/run/secrets/kubernetes.io/serviceaccount/token",
    ]

    token = run_command_in_vault(vault_namespace, vault_name, cmd, expected_message=[])
    cmd = ["env"]

    response = run_command_in_vault(vault_namespace, vault_name, cmd, expected_message=[])

    response = response.split("\n")
    for line in response:
        l = line.strip()
        if str.startswith(l, "KUBERNETES_PORT_443_TCP_ADDR"):
            cluster_ip = l.split("=")[1]
            break
    cmd = [
        "vault",
        "write",
        "auth/kubernetes/config",
        f"token_reviewer_jwt={token}",
        f"kubernetes_host=https://{cluster_ip}:443",
        "kubernetes_ca_cert=@/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
        "disable_iss_validation=true",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om_backup
def test_enable_vault_role_for_operator_pod(
    vault_name: str,
    vault_namespace: str,
    namespace: str,
    vault_operator_policy_name: str,
):
    cmd = [
        "vault",
        "write",
        "auth/kubernetes/role/mongodbenterprise",
        f"bound_service_account_names={OPERATOR_NAME}",
        f"bound_service_account_namespaces={namespace}",
        f"policies={vault_operator_policy_name}",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om_backup
def test_put_admin_credentials_to_vault(namespace: str, vault_namespace: str, vault_name: str):
    admin_credentials_secret_name = "ops-manager-admin-secret"
    # read the -admin-secret from namespace and store in vault
    data = read_secret(namespace, admin_credentials_secret_name)
    path = f"secret/mongodbenterprise/operator/{namespace}/{admin_credentials_secret_name}"
    store_secret_in_vault(vault_namespace, vault_name, data, path)
    delete_secret(namespace, admin_credentials_secret_name)


@mark.e2e_vault_setup_om_backup
def test_operator_install_with_vault_backend(operator_vault_secret_backend: Operator):
    operator_vault_secret_backend.assert_is_running()


@mark.e2e_vault_setup_om_backup
def test_create_appdb_policy(vault_name: str, vault_namespace: str):
    KubernetesTester.copy_file_inside_pod(
        f"{vault_name}-0",
        "vaultpolicies/appdb-policy.hcl",
        "/tmp/appdb-policy.hcl",
        namespace=vault_namespace,
    )

    cmd = [
        "vault",
        "policy",
        "write",
        "mongodbenterpriseappdb",
        "/tmp/appdb-policy.hcl",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om_backup
def test_create_om_policy(vault_name: str, vault_namespace: str):
    KubernetesTester.copy_file_inside_pod(
        f"{vault_name}-0",
        "vaultpolicies/opsmanager-policy.hcl",
        "/tmp/om-policy.hcl",
        namespace=vault_namespace,
    )

    cmd = [
        "vault",
        "policy",
        "write",
        "mongodbenterpriseopsmanager",
        "/tmp/om-policy.hcl",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om_backup
def test_enable_vault_role_for_appdb_pod(
    vault_name: str,
    vault_namespace: str,
    namespace: str,
    vault_appdb_policy_name: str,
):
    cmd = [
        "vault",
        "write",
        f"auth/kubernetes/role/{vault_appdb_policy_name}",
        f"bound_service_account_names={APPDB_SA_NAME}",
        f"bound_service_account_namespaces={namespace}",
        f"policies={vault_appdb_policy_name}",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om_backup
def test_enable_vault_role_for_om_pod(
    vault_name: str,
    vault_namespace: str,
    namespace: str,
    vault_om_policy_name: str,
):
    cmd = [
        "vault",
        "write",
        f"auth/kubernetes/role/{vault_om_policy_name}",
        f"bound_service_account_names={OM_SA_NAME}",
        f"bound_service_account_namespaces={namespace}",
        f"policies={vault_om_policy_name}",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om_backup
def test_create_database_policy(vault_name: str, vault_namespace: str):
    KubernetesTester.copy_file_inside_pod(
        f"{vault_name}-0",
        "vaultpolicies/database-policy.hcl",
        "/tmp/database-policy.hcl",
        namespace=vault_namespace,
    )

    cmd = [
        "vault",
        "policy",
        "write",
        "mongodbenterprisedatabase",
        "/tmp/database-policy.hcl",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om_backup
def test_enable_vault_role_for_database_pod(
    vault_name: str,
    vault_namespace: str,
    namespace: str,
    vault_database_policy_name: str,
):
    cmd = [
        "vault",
        "write",
        f"auth/kubernetes/role/{vault_database_policy_name}",
        f"bound_service_account_names={DATABASE_SA_NAME}",
        f"bound_service_account_namespaces={namespace}",
        f"policies={vault_database_policy_name}",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om_backup
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.backup_status().assert_reaches_phase(
        Phase.Pending,
        msg_regexp="The MongoDB object .+ doesn't exist",
        timeout=900,
    )


@mark.e2e_vault_setup_om_backup
def test_prometheus_endpoint_works_on_every_pod_on_appdb(ops_manager: MongoDB):
    auth = ("prom-user", "prom-password")
    name = ops_manager.name + "-db"

    for idx in range(ops_manager["spec"]["applicationDatabase"]["members"]):
        url = f"https://{name}-{idx}.{name}-svc.{ops_manager.namespace}.svc.cluster.local:9216/metrics"
        assert https_endpoint_is_reachable(url, auth, tls_verify=False)


@mark.e2e_vault_setup_om_backup
def test_backup_mdbs_created(
    oplog_replica_set: MongoDB,
    s3_replica_set: MongoDB,
):
    """Creates mongodb databases all at once"""
    oplog_replica_set.assert_reaches_phase(Phase.Running)
    s3_replica_set.assert_reaches_phase(Phase.Running)


@mark.e2e_vault_setup_om_backup
def test_om_backup_running(ops_manager: MongoDBOpsManager):
    ops_manager.backup_status().assert_reaches_phase(
        Phase.Running,
    )


@mark.e2e_vault_setup_om_backup
def test_no_admin_key_secret_in_kubernetes(namespace: str, ops_manager: MongoDBOpsManager):
    with pytest.raises(ApiException):
        read_secret(namespace, f"{namespace}-{ops_manager.name}-admin-key")


@mark.e2e_vault_setup_om_backup
def test_appdb_reached_running_and_pod_count(ops_manager: MongoDBOpsManager, namespace: str):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
    # check AppDB has 4 containers(+1 because of vault-agent)
    for pod_name in get_pods(ops_manager.name + "-db-{}", 3):
        pod = client.CoreV1Api().read_namespaced_pod(pod_name, namespace)
        assert len(pod.spec.containers) == 4


@mark.e2e_vault_setup_om_backup
def test_no_s3_credentials__secret_in_kubernetes(namespace: str, ops_manager: MongoDBOpsManager):
    with pytest.raises(ApiException):
        read_secret(
            namespace,
            S3_SECRET_NAME,
        )
