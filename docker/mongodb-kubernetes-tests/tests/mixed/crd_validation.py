"""
Checks that the CRD conform to Structural Schema:
https://kubernetes.io/docs/tasks/access-kubernetes-api/custom-resources/custom-resource-definitions/#specifying-a-structural-schema
"""

from kubernetes.client import ApiextensionsV1Api, V1CustomResourceDefinition
from pytest import mark


def crd_has_expected_conditions(resource: V1CustomResourceDefinition) -> bool:
    for condition in resource.status.conditions:
        if condition.type == "NonStructuralSchema":
            return False

    return True


@mark.e2e_crd_validation
def test_mongodb_crd_is_valid(crd_api: ApiextensionsV1Api):
    resource = crd_api.read_custom_resource_definition("mongodb.mongodb.com")
    assert crd_has_expected_conditions(resource)


@mark.e2e_crd_validation
def test_mongodb_users_crd_is_valid(crd_api: ApiextensionsV1Api):
    resource = crd_api.read_custom_resource_definition("mongodbusers.mongodb.com")
    assert crd_has_expected_conditions(resource)


@mark.e2e_crd_validation
def test_opsmanagers_crd_is_valid(crd_api: ApiextensionsV1Api):
    resource = crd_api.read_custom_resource_definition("opsmanagers.mongodb.com")
    assert crd_has_expected_conditions(resource)


@mark.e2e_crd_validation
def test_mongodbmulti_crd_is_valid(crd_api: ApiextensionsV1Api):
    resource = crd_api.read_custom_resource_definition("mongodbmulticluster.mongodb.com")
    assert crd_has_expected_conditions(resource)


@mark.e2e_crd_validation
def test_cluster_mongodb_roles_crd_is_valid(crd_api: ApiextensionsV1Api):
    resource = crd_api.read_custom_resource_definition("clustermongodbroles.mongodb.com")
    assert crd_has_expected_conditions(resource)
