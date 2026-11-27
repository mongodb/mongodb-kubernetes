import pytest
from tests.common.voyageai.echo_stub import echo_stub_image


@pytest.fixture(scope="module")
def stub_image(operator_installation_config: dict[str, str]) -> str:
    return echo_stub_image(operator_installation_config)
