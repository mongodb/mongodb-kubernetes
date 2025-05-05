import time

from kubernetes import client
from kubernetes.client import ApiException
from tests import test_logger
from tests.conftest import log_deployments_info

logger = test_logger.get_test_logger(__name__)


# Scale down a deployment to 0. This is useful for upgrade tests as long as they install two different operators in
# parallel (MEKO and MCK)
def downscale_operator_deployment(deployment_name: str, namespace: str):
    DOWNSCALE_TO = 0
    log_deployments_info(namespace)
    logger.info(f"Attempting to downscale deployment '{deployment_name}' in namespace '{namespace}'")

    apps_v1 = client.AppsV1Api()
    body = {"spec": {"replicas": DOWNSCALE_TO}}
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

    # We should wait for scale down to avoid flakes
    logger.info(f"Waiting for '{deployment_name}' to scale down to {DOWNSCALE_TO}...")
    for i in range(10):
        try:
            deployment = apps_v1.read_namespaced_deployment(name=deployment_name, namespace=namespace)
            replicas = deployment.status.replicas or 0
            ready_replicas = deployment.status.ready_replicas or 0
            if replicas == 0 and ready_replicas == DOWNSCALE_TO:
                logger.info(f"'{deployment_name}' successfully downscaled")
                return
        except ApiException as e:
            if e.status == 404:
                logger.warning(f"'{deployment_name}' not found while waiting for downscale")
                return
            else:
                logger.error(f"Error while waiting for downscale: {e}")
                raise

        time.sleep(2)

    logger.warning(f"Timeout while waiting for '{deployment_name}' to downscale")
