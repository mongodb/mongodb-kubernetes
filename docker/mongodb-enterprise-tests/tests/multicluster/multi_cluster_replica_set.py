from typing import Dict

import pytest

from kubetester.operator import Operator


@pytest.mark.e2e_multi_cluster_replica_set
def test_create_kube_config_file(cluster_clients: Dict):
    clients = cluster_clients

    assert len(clients) == 3
    assert "e2e.cluster1.mongokubernetes.com" in clients
    assert "e2e.cluster2.mongokubernetes.com" in clients
    assert "e2e.operator.mongokubernetes.com" in clients


@pytest.mark.e2e_multi_cluster_replica_set
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()
