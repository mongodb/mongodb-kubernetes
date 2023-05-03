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


@pytest.mark.e2e_olm_operator_upgrade
def test_upgrade_operator_only(namespace: str, version_id: str):
    current_operator_version = get_current_operator_version()
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
            "channel": "stable",
            "name": "mongodb-enterprise",
            "source": catalog_source_resource.name,
            "sourceNamespace": namespace,
            "installPlanApproval": "Automatic",
        },
    )

    create_or_update(subscription)

    wait_for_operator_ready(namespace, f"mongodb-enterprise.v{current_operator_version}")

    subscription.load()
    subscription["spec"]["channel"] = "fast"
    subscription.update()

    wait_for_operator_ready(namespace, f"mongodb-enterprise.v{incremented_operator_version}")
