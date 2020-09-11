import pytest
from kubetester.operator import Operator


@pytest.mark.e2e_operator_upgrade_replica_set
def test_install_latest_operator(official_operator: Operator):
    official_operator.assert_is_running()
