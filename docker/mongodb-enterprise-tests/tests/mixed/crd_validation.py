"""
Checks that the CRD conform to Structural Schema:
https://kubernetes.io/docs/tasks/access-kubernetes-api/custom-resources/custom-resource-definitions/#specifying-a-structural-schema
"""

from pytest import mark, fixture

from kubernetes.client import ApiextensionsV1beta1Api, V1beta1CustomResourceDefinition


def crd_has_expected_conditions(resource: V1beta1CustomResourceDefinition) -> bool:
    for condition in resource.status.conditions:
        if condition.type == "NonStructuralSchema":
            return False

    return True


@mark.e2e_crd_validation
def test_mongodb_crd_is_valid(crd_api: ApiextensionsV1beta1Api):
    resource = crd_api.read_custom_resource_definition("mongodb.mongodb.com")
    assert crd_has_expected_conditions(resource)


@mark.e2e_crd_validation
def test_mongodb_users_crd_is_valid(crd_api: ApiextensionsV1beta1Api):
    resource = crd_api.read_custom_resource_definition("mongodbusers.mongodb.com")
    assert crd_has_expected_conditions(resource)


@mark.e2e_crd_validation
def test_opsmanagers_crd_is_valid(crd_api: ApiextensionsV1beta1Api):
    resource = crd_api.read_custom_resource_definition("opsmanagers.mongodb.com")
    assert crd_has_expected_conditions(resource)
