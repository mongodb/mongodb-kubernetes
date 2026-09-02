from typing import Dict, List

import kubernetes
from kubernetes import client
from kubetester.kubetester import running_locally
from pytest import fixture
from tests.conftest import _get_client_for_cluster
from tests.multicluster_decentralized.installer import InstallerSettings, settings_from_env


@fixture(scope="module")
def decentralized_settings() -> InstallerSettings:
    return settings_from_env()


@fixture(scope="module")
def decentralized_cluster_clients(decentralized_settings: InstallerSettings) -> Dict[str, client.ApiClient]:
    """One ApiClient per cluster, keyed by cluster (context) name. There is no central cluster:
    every cluster gets the same treatment. Host-run uses the ambient kubeconfig contexts; in-pod
    runs use the token-file machinery the rest of the multi-cluster harness uses."""
    if running_locally():
        return {c: kubernetes.config.new_client_from_config(context=c) for c in decentralized_settings.clusters}
    return {c: _get_client_for_cluster(c) for c in decentralized_settings.clusters}
