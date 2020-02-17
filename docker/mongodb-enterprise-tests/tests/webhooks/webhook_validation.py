import yaml
import pytest
from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester


@pytest.mark.e2e_webhook_validation
class TestWebhookValidation(KubernetesTester):
    def test_horizons_tls_validation(self):
        resource = yaml.safe_load(
            open(yaml_fixture("invalid_replica_set_horizons.yaml"))
        )
        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="TLS must be enabled in order to use replica set horizons",
        )

    @pytest.mark.skip(
        reason="Validations are currently not configured to run on reconciliation"
    )
    def test_validates_without_webhook(self):
        webhook_name = "mdbpolicy.mongodb.com"
        webhook_api = self.client.AdmissionregistrationV1beta1Api()

        # break the existing webhook
        webhook = webhook_api.read_validating_webhook_configuration(webhook_name)
        old_webhooks = webhook.webhooks
        webhook.webhooks[0].client_config.service.name = "a-non-existent-service"
        webhook.metadata.uid = ""
        webhook_api.replace_validating_webhook_configuration(webhook_name, webhook)

        # check that the webhook doesn't block and that the resource gets into
        # the errored state
        resource = yaml.safe_load(
            open(yaml_fixture("invalid_replica_set_horizons.yaml"))
        )
        self.create_custom_resource_from_object(self.get_namespace(), resource)
        KubernetesTester.wait_until("in_error_state", 20)

        # fix webhooks
        webhook = webhook_api.read_validating_webhook_configuration(webhook_name)
        webhook.webhooks = old_webhooks
        webhook.metadata.uid = ""
        webhook_api.replace_validating_webhook_configuration(webhook_name, webhook)
