from _pytest.fixtures import fixture
from kubetester.operator import Operator
from tests.conftest import (
    get_central_cluster_client,
    get_central_cluster_name,
    get_default_operator,
    get_member_cluster_clients,
    get_member_cluster_names,
    get_multi_cluster_operator,
    get_multi_cluster_operator_installation_config,
    get_operator_installation_config,
    is_multi_cluster,
)


@fixture(scope="module")
def operator(namespace: str) -> Operator:
    if is_multi_cluster():
        return get_multi_cluster_operator(
            namespace,
            get_central_cluster_name(),
            get_multi_cluster_operator_installation_config(namespace),
            get_central_cluster_client(),
            get_member_cluster_clients(),
            get_member_cluster_names(),
        )
    else:
        return get_default_operator(namespace, get_operator_installation_config(namespace))
