from __future__ import annotations

import re
from os import path

import yaml
from kubernetes import client
from kubernetes.client import ApiClient
from kubernetes.utils.create_from_yaml import create_from_yaml_single_item

"""
This is a modification of 'create_from_yaml.py' from python kubernetes client library
It allows to mimic the 'kubectl apply' operation on yaml file. It performs either create or 
patch on each individual object.
"""


def create_or_replace_from_yaml(k8s_client: ApiClient, yaml_file: str, namespace: str = "default", **kwargs):
    with open(path.abspath(yaml_file)) as f:
        yml_document_all = yaml.safe_load_all(f)
        # Load all documents from a single YAML file
        for yml_document in yml_document_all:
            create_or_patch_from_dict(k8s_client, yml_document, namespace=namespace, **kwargs)


def create_or_patch_from_dict(k8s_client, yml_document, namespace="default", **kwargs):
    # If it is a list type, will need to iterate its items
    if "List" in yml_document["kind"]:
        # Could be "List" or "Pod/Service/...List"
        # This is a list type. iterate within its items
        kind = yml_document["kind"].replace("List", "")
        for yml_object in yml_document["items"]:
            # Mitigate cases when server returns a xxxList object
            # See kubernetes-client/python#586
            if kind != "":
                yml_object["apiVersion"] = yml_document["apiVersion"]
                yml_object["kind"] = kind
                create_or_replace_from_yaml_single_item(k8s_client, yml_object, namespace, **kwargs)
    else:
        # Try to create the object or patch if it already exists
        create_or_replace_from_yaml_single_item(k8s_client, yml_document, namespace, **kwargs)


def create_or_replace_from_yaml_single_item(k8s_client, yml_object, namespace="default", **kwargs):
    try:
        create_from_yaml_single_item(k8s_client, yml_object, verbose=False, namespace=namespace, **kwargs)
    except client.rest.ApiException:
        patch_from_yaml_single_item(k8s_client, yml_object, namespace, **kwargs)
    except ValueError:
        if get_kind(yml_object) == "custom_resource_definition":
            # TODO unfortunately CRD creation results in error before 1.12 python lib and 1.16 K8s
            # https://github.com/kubernetes-client/python/issues/1022
            pass


def patch_from_yaml_single_item(k8s_client, yml_object, namespace="default", **kwargs):
    k8s_api = get_k8s_api(k8s_client, yml_object)
    kind = get_kind(yml_object)
    # Decide which namespace we are going to put the object in,
    # if any
    if "namespace" in yml_object["metadata"]:
        namespace = yml_object["metadata"]["namespace"]
    name = yml_object["metadata"]["name"]

    method = "patch"
    if kind == "custom_resource_definition":
        # fetching the old CRD to make the replace working (has conflict resolution based on 'resourceVersion')
        # TODO this is prone to race conditions - we need to either loop or use patch with json merge
        # see https://github.com/helm/helm/pull/6092/files#diff-a483d6c0863082c3df21f4aad513afe2R663
        resource = client.ApiextensionsV1Api().read_custom_resource_definition(name)

        yml_object["metadata"]["resourceVersion"] = resource.metadata.resource_version
        method = "replace"

    namespaced = hasattr(k8s_api, "{}_namespaced_{}".format("create", kind))
    url_path = get_url_path(namespaced, method)

    if namespaced:
        # Note that patch the deployment can result in
        # "Invalid value: \"\": may not be specified when `value` is not empty","field":"spec.template.spec.containers[0].env[1].valueFrom"
        # (https://github.com/kubernetes/kubernetes/issues/46861) if "kubectl apply" was used initially to create the object
        # This is safe though if the object was created using python API
        getattr(k8s_api, url_path.format(kind))(body=yml_object, namespace=namespace, name=name, **kwargs)
    else:
        # "patch" endpoints require to specify 'name' attribute
        getattr(k8s_api, url_path.format(kind))(body=yml_object, name=name, **kwargs)


def get_kind(yml_object):
    # Replace CamelCased action_type into snake_case
    kind = yml_object["kind"]
    kind = re.sub("(.)([A-Z][a-z]+)", r"\1_\2", kind)
    kind = re.sub("([a-z0-9])([A-Z])", r"\1_\2", kind).lower()
    return kind


def get_k8s_api(k8s_client, yml_object):
    group, _, version = yml_object["apiVersion"].partition("/")
    if version == "":
        version = group
        group = "core"
    # Take care for the case e.g. api_type is "apiextensions.k8s.io"
    # Only replace the last instance
    group = "".join(group.rsplit(".k8s.io", 1))
    # convert group name from DNS subdomain format to
    # python class name convention
    group = "".join(word.capitalize() for word in group.split("."))
    fcn_to_call = "{0}{1}Api".format(group, version.capitalize())
    k8s_api = getattr(client, fcn_to_call)(k8s_client)
    return k8s_api


def get_url_path(namespaced, method: str):
    if namespaced:
        url_path = method + "_namespaced_{}"
    else:
        url_path = method + "_{}"
    return url_path
