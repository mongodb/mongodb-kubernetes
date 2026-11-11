"""
Ensures that the Ops Manager validation webhook installed with the operator rejects invalid resources
"""

import pytest
from kubernetes.client.rest import ApiException
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import mark


@mark.e2e_om_validation_webhook
def test_wait_for_webhook(namespace: str, default_operator: Operator):
    default_operator.wait_for_operator_webhook_ready()


@mark.e2e_om_validation_webhook
def test_opsmanager_version(namespace: str):
    om = MongoDBOpsManager.from_yaml(yaml_fixture("om_validation.yaml"), namespace=namespace)
    om["spec"]["version"] = "4.4.4.4"

    with pytest.raises(ApiException, match=r"is an invalid value for spec.version"):
        om.create()
