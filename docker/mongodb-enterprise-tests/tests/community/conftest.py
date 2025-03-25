from kubetester.operator import Operator
from pytest import fixture
from tests.conftest import get_default_operator, get_operator_installation_config


@fixture(scope="module")
def operator(namespace: str) -> Operator:
    helm_args = get_operator_installation_config(namespace)
    # TODO: MCK We may want to always watch community resources by default with MCK but it implies to always have
    #  community CRD installed. In that case we wouldn't need this custom install function, we can get rid of it
    #  once we merge the helm charts
    helm_args["operator.watchedResources"] = "{opsmanagers,mongodb,mongodbusers,mongodbcommunity}"
    return get_default_operator(namespace, helm_args)
