#!/usr/bin/env python3
import os.path
import sys
from typing import Dict

from dev_config import DevConfig, Distro, load_config


def _get_e2e_test_envs(dev_config: DevConfig) -> Dict[str, str]:
    """
    _get_e2e_test_envs returns a dictionary of all the required environment variables
    that need to be set in order to run a local e2e test.

    :param dev_config: The local dev config
    :return: A diction of env vars to be set
    """
    cleanup = False
    if len(sys.argv) > 1:
        cleanup = sys.argv[1] == "true"
    patch_id = os.getenv("version_id")
    operator_image = f"{dev_config.repo_url}/{dev_config.operator_image}:{patch_id}"
    return {
        # TODO: MCK merge this with make switch
        # TODO: MCK commented out means using default from testConfig.go
        "ROLE_DIR": dev_config.role_dir,
        "DEPLOY_DIR": dev_config.deploy_dir,
        "OPERATOR_IMAGE": operator_image,
        # TODO: MCK use root helm chart
        "HELM_CHART_PATH": os.path.abspath("./mongodb-community-operator/helm-charts/charts/enterprise-operator"),
        # "VERSION_UPGRADE_HOOK_IMAGE": f"{dev_config.repo_url}/{dev_config.version_upgrade_hook_image}",
        # "READINESS_PROBE_IMAGE": f"{dev_config.repo_url}/{dev_config.readiness_probe_image}",
        # TODO: MCK make this configurable but default to /dev not /dev/user image
        "MONGODB_COMMUNITY_AGENT_IMAGE": f"{dev_config.shared_repo_url}/{dev_config.agent_image}:108.0.2.8729-1",
        "TEST_DATA_DIR": dev_config.test_data_dir,
        "TEST_NAMESPACE": dev_config.namespace,
        "PERFORM_CLEANUP": "true" if cleanup else "false",
        "WATCH_NAMESPACE": dev_config.namespace,
        "MONGODB_COMMUNITY_IMAGE": dev_config.mongodb_image_name,
        "MONGODB_REPO_URL": dev_config.mongodb_image_repo_url,
        "MDB_IMAGE_TYPE": dev_config.image_type,
        "MDB_LOCAL_OPERATOR": dev_config.local_operator,
        "KUBECONFIG": dev_config.kube_config,
    }


# convert all values in config.json to env vars.
# this can be used to provide configuration for e2e tests.
def main(config_json) -> int:
    dev_config = load_config(distro=Distro.UBI, config_file_path=config_json)
    for k, v in _get_e2e_test_envs(dev_config).items():
        print(f"export {k.upper()}={v}")
    return 0


if __name__ == "__main__":
    input_arg = sys.argv[1] if len(sys.argv) > 1 else None
    sys.exit(main(input_arg))
