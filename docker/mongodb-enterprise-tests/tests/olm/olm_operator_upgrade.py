import pytest
from kubernetes.client.rest import ApiException
from kubetester import MongoDB, read_service, wait_for_webhook
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import (
    get_default_architecture,
    is_default_architecture_static,
)
from kubetester.opsmanager import MongoDBOpsManager
from tests.olm.olm_test_commons import (
    get_catalog_image,
    get_catalog_source_resource,
    get_current_operator_version,
    get_latest_released_operator_version,
    get_operator_group_resource,
    get_subscription_custom_object,
    increment_patch_version,
    wait_for_operator_ready,
)

# See docs how to run this locally: https://wiki.corp.mongodb.com/display/MMS/E2E+Tests+Notes#E2ETestsNotes-OLMtests

# This tests only OLM upgrade of the operator without deploying any resources.


@pytest.mark.e2e_olm_operator_upgrade
def test_upgrade_operator_only(namespace: str, version_id: str):
    latest_released_operator_version = get_latest_released_operator_version("mongodb-kubernetes")
    current_operator_version = get_current_operator_version()
    incremented_operator_version = increment_patch_version(current_operator_version)

    get_operator_group_resource(namespace, namespace).update()
    catalog_source_resource = get_catalog_source_resource(
        namespace, get_catalog_image(f"{incremented_operator_version}-{version_id}")
    )
    catalog_source_resource.update()

    static_value = get_default_architecture()
    subscription = get_subscription_custom_object(
        "mongodb-kubernetes",
        namespace,
        {
            "channel": "stable",  # stable channel contains latest released operator in RedHat's certified repository
            "name": "mongodb-enterprise",
            "source": catalog_source_resource.name,
            "sourceNamespace": namespace,
            "installPlanApproval": "Automatic",
            # In certified OpenShift bundles we have this enabled, so the operator is not defining security context (it's managed globally by OpenShift).
            # In Kind this will result in empty security contexts and problems deployments with filesystem permissions.
            "config": {
                "env": [
                    {"name": "MANAGED_SECURITY_CONTEXT", "value": "false"},
                    {"name": "OPERATOR_ENV", "value": "dev"},
                    {"name": "MDB_DEFAULT_ARCHITECTURE", "value": static_value},
                    {"name": "MDB_OPERATOR_TELEMETRY_SEND_ENABLED", "value": "false"},
                ]
            },
        },
    )

    subscription.update()

    wait_for_operator_ready(namespace, "mongodb-kubernetes", f"mongodb-kubernetes.v{latest_released_operator_version}")

    subscription.load()
    subscription["spec"]["channel"] = "fast"  # fast channel contains operator build from the current branch
    subscription.update()

    wait_for_operator_ready(namespace, "mongodb-kubernetes", f"mongodb-kubernetes.v{incremented_operator_version}")


@pytest.mark.e2e_olm_operator_upgrade
def test_operator_webhook_is_deleted_and_not_installed_anymore(namespace: str):
    # in the first release of OLM webhooks, the previous version will have this webhook installed
    # in subsequent releases we will only test here that it's no longer installed
    with pytest.raises(ApiException) as e:
        read_service(namespace, "operator-webhook")
    assert e.value.status == 404


@pytest.mark.e2e_olm_operator_upgrade
def test_wait_for_webhook(namespace: str):
    wait_for_webhook(namespace=namespace, service_name="mongodb-enterprise-operator-service")


@pytest.mark.e2e_olm_operator_upgrade
def test_opsmanager_webhook(namespace: str):
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_validation.yaml"), namespace=namespace)
    resource["spec"]["version"] = "4.4.4.4"

    with pytest.raises(ApiException, match=r"is an invalid value for spec.version"):
        resource.create()


@pytest.mark.e2e_olm_operator_upgrade
def test_mongodb_webhook(namespace: str):
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), namespace=namespace)
    resource["spec"]["members"] = 0

    with pytest.raises(ApiException, match=r"'spec.members' must be specified if"):
        resource.create()
