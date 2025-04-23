from kubernetes import client
from kubernetes.client import ApiException

from tests import test_logger
from tests.conftest import log_deployments_info

logger = test_logger.get_test_logger(__name__)

# Scale down a deployment to 0. This is useful for upgrade tests as long as they install two different operators in
# parallel (MEKO and MCK)
def downscale_operator_deployment(deployment_name:str, namespace: str):
    log_deployments_info(namespace)
    logger.info(f"Attempting to downscale deployment '{deployment_name}' in namespace '{namespace}'")

    apps_v1 = client.AppsV1Api()
    body = {"spec": {"replicas": 0}}
    # We need to catch not found exception to be future-proof
    try:
        # Attempt to patch the deployment scale
        apps_v1.patch_namespaced_deployment_scale(name=deployment_name, namespace=namespace, body=body)
        logger.info(f"Successfully downscaled {deployment_name}")
    except ApiException as e:
        if e.status == 404:
            logger.warning(f"'{deployment_name}' not found in namespace '{namespace}'. Skipping downscale")
        else:
            logger.error(f"Unexpected error: {e}")
            raise
