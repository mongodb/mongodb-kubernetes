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

    def test_horizons_members(self):
        resource = yaml.safe_load(open(yaml_fixture("invalid_replica_set_horizons_members.yaml")))
        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="Number of horizons must be equal to number of members in replica set",
        )

    def test_x509_without_tls(self):
        resource = yaml.safe_load(open(yaml_fixture("invalid_replica_set_x509_no_tls.yaml")))
        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="Cannot have a non-tls deployment when x509 authentication is enabled",
        )

    def test_auth_without_modes(self):
        resource = yaml.safe_load(open(yaml_fixture("invalid_replica_set_agent_auth_not_in_modes.yaml")))
        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="Cannot configure an Agent authentication mechanism that is not specified in authentication modes",
        )

    def test_agent_auth_enabled_with_no_modes(self):
        resource = yaml.safe_load(open(yaml_fixture("invalid_replica_set_auth_no_modes.yaml")))
        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="Cannot enable authentication without modes specified",
        )

    def test_ldap_auth_with_mongodb_community(self):
        resource = yaml.safe_load(open(yaml_fixture("invalid_replica_set_ldap_community.yaml")))
        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="Cannot enable LDAP authentication with MongoDB Community Builds",
        )

    def test_no_agent_auth_mode_with_multiple_modes_enabled(self):
        resource = yaml.safe_load(open(yaml_fixture("invalid_replica_set_no_agent_mode.yaml")))
        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="spec.security.authentication.agents.mode must be specified if more than one entry is present in spec.security.authentication.modes",
        )

    def test_ldap_auth_with_no_ldapgroupdn(self):
        resource = yaml.safe_load(open(yaml_fixture("invalid_replica_set_ldapauthz_no_ldapgroupdn.yaml")))
        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="automationLdapGroupDN must be specified if LDAP authorization is used and agent auth mode is $external (x509 or LDAP)",
        )

    def test_replicaset_members_is_specified(self):
        resource = yaml.safe_load(open(yaml_fixture("invalid_mdb_member_count.yaml")))

        self.create_custom_resource_from_object(
            self.get_namespace(),
            resource,
            exception_reason="'spec.members' must be specified if type of MongoDB is ReplicaSet",
        )

    def test_replicaset_members_is_specified_without_webhook(self):
        self._assert_validates_without_webhook(
            "mdbpolicy.mongodb.com",
            "invalid_mdb_member_count.yaml",
            "'spec.members' must be specified if type of MongoDB is ReplicaSet",
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

    def _assert_validates_without_webhook(self, webhook_name: str, fixture: str, expected_msg: str):
        webhook_api = self.client.AdmissionregistrationV1Api()

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
