from kubetester import create_or_update
from tests.olm.olm_test_commons import (
    get_current_operator_version,
    increment_patch_version,
    get_operator_group_resource,
    get_catalog_source_resource,
    get_catalog_image,
    get_subscription_custom_object,
    wait_for_operator_ready,
)
import pytest


# See docs how to run this locally: https://wiki.corp.mongodb.com/display/MMS/E2E+Tests+Notes#E2ETestsNotes-OLMtests

# This tests only OLM upgrade of the operator without deploying any resources.


@pytest.mark.e2e_olm_operator_upgrade
def test_upgrade_operator_only(namespace: str, version_id: str):
    current_operator_version = get_current_operator_version(namespace)
    incremented_operator_version = increment_patch_version(current_operator_version)

    create_or_update(get_operator_group_resource(namespace, namespace))
    catalog_source_resource = get_catalog_source_resource(
        namespace, get_catalog_image(f"{incremented_operator_version}-{version_id}")
    )
    create_or_update(catalog_source_resource)

    subscription = get_subscription_custom_object(
        "mongodb-enterprise-operator",
        namespace,
        {
            "channel": "stable",  # stable channel contains latest released operator in RedHat's certified repository
            "name": "mongodb-enterprise",
            "source": catalog_source_resource.name,
            "sourceNamespace": namespace,
            "installPlanApproval": "Automatic",
            # In certified OpenShift bundles we have this enabled, so the operator is not defining security context (it's managed globally by OpenShift).
            # In Kind this will result in empty security contexts and problems deployments with filesystem permissions.
            "config": {"env": [{"name": "MANAGED_SECURITY_CONTEXT", "value": "false"}]},
        },
    )

    create_or_update(subscription)

    wait_for_operator_ready(namespace, f"mongodb-enterprise.v{current_operator_version}")

    subscription.load()
    subscription["spec"]["channel"] = "fast"  # fast channel contains operator build from the current branch
    subscription.update()

    wait_for_operator_ready(namespace, f"mongodb-enterprise.v{incremented_operator_version}")
