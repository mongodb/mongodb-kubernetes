import pytest
from typing import List


@pytest.mark.e2e_multi_cluster_tls
def test_deploy_cert_manager_member_clusters(multi_cluster_issuer: str):
    multi_cluster_issuer
