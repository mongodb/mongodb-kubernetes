import kubernetes
from kubetester.awss3client import s3_endpoint
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from tests.common.constants import S3_BLOCKSTORE_NAME, S3_OPLOG_NAME
from tests.constants import AWS_REGION


def ops_manager_multi_cluster_with_tls_s3_backups(
    namespace: str,
    name: str,
    central_cluster_client: kubernetes.client.ApiClient,
    custom_appdb_version: str,
    s3_bucket_blockstore: str,
    s3_bucket_oplog: str,
):
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup_tls_s3.yaml"), name=name, namespace=namespace
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.allow_mdb_rc_versions()
    resource.set_appdb_version(custom_appdb_version)

    # configure S3 Blockstore
    resource["spec"]["backup"]["s3Stores"][0]["name"] = S3_BLOCKSTORE_NAME
    resource["spec"]["backup"]["s3Stores"][0]["s3SecretRef"]["name"] = S3_BLOCKSTORE_NAME + "-secret"
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketEndpoint"] = s3_endpoint(AWS_REGION)
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket_blockstore
    resource["spec"]["backup"]["s3Stores"][0]["s3RegionOverride"] = AWS_REGION
    # configure S3 Oplog
    resource["spec"]["backup"]["s3OpLogStores"][0]["name"] = S3_OPLOG_NAME
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3SecretRef"]["name"] = S3_OPLOG_NAME + "-secret"
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3BucketEndpoint"] = s3_endpoint(AWS_REGION)
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3BucketName"] = s3_bucket_oplog
    resource["spec"]["backup"]["s3OpLogStores"][0]["s3RegionOverride"] = AWS_REGION

    # configure memory overrides so OM doesn't crash
    resource["spec"]["statefulSet"] = {
        "spec": {
            "template": {
                "spec": {
                    "containers": [
                        {
                            "name": "mongodb-ops-manager",
                            "resources": {"requests": {"memory": "15G"}, "limits": {"memory": "15G"}},
                        },
                    ]
                }
            }
        }
    }
    resource.create_admin_secret(api_client=central_cluster_client)

    return resource
