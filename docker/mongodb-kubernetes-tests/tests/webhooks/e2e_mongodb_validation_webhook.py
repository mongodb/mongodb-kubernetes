import pytest
import yaml
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture


@pytest.mark.e2e_mongodb_validation_webhook
class TestWebhookValidation(KubernetesTester):
    def test_horizons_tls_validation(self):
        resource = yaml.safe_load(open(yaml_fixture("invalid_replica_set_horizons_tls.yaml")))
        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="TLS must be enabled in order to use replica set horizons",
        )
