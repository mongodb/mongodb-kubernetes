import yaml
import pytest
from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester


@pytest.mark.e2e_webhook_validation
class TestWebhookValidation(KubernetesTester):
    def test_horizons_tls_validation(self):
        resource = yaml.safe_load(
            open(yaml_fixture("invalid_replica_set_horizons_tls.yaml"))
        )
        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="TLS must be enabled in order to use replica set horizons",
        )

    def test_horizons_members(self):
        resource = yaml.safe_load(
            open(yaml_fixture("invalid_replica_set_horizons_members.yaml"))
        )
        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="Number of horizons must be equal to number of members in replica set",
        )

    def test_horizons_without_tls_validates_without_webhook(self):
        self._assert_validates_without_webhook(
            "mdbpolicy.mongodb.com",
            "invalid_replica_set_horizons_tls.yaml",
            "TLS must be enabled",
        )

    def test_incorrect_members_validates_without_webhook(self):
        self._assert_validates_without_webhook(
            "mdbpolicy.mongodb.com",
            "invalid_replica_set_horizons_members.yaml",
            "number of members",
        )

    def _assert_validates_without_webhook(
        self, webhook_name: str, fixture: str, expected_msg: str
    ):
        webhook_api = self.client.AdmissionregistrationV1beta1Api()

        # break the existing webhook
        webhook = webhook_api.read_validating_webhook_configuration(webhook_name)
        old_webhooks = webhook.webhooks
        webhook.webhooks[0].client_config.service.name = "a-non-existent-service"
        webhook.metadata.uid = ""
        webhook_api.replace_validating_webhook_configuration(webhook_name, webhook)

        # check that the webhook doesn't block and that the resource gets into
        # the errored state
        resource = yaml.safe_load(open(yaml_fixture(fixture)))
        self.create_custom_resource_from_object(self.get_namespace(), resource)
        KubernetesTester.wait_until("in_error_state", 20)
        mrs = KubernetesTester.get_resource()
        assert expected_msg in mrs["status"]["message"]

        # fix webhooks
        webhook = webhook_api.read_validating_webhook_configuration(webhook_name)
        webhook.webhooks = old_webhooks
        webhook.metadata.uid = ""
        webhook_api.replace_validating_webhook_configuration(webhook_name, webhook)
