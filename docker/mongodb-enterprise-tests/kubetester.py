from kubernetes import client, config
from kubernetes.client.rest import ApiException
import time
import yaml
from os import getenv
import requests
from requests.auth import HTTPDigestAuth
import jsonpatch


class KubernetesTester(object):
    """Tests a kubernetes object application."""

    @staticmethod
    def clients(name):
        return {
            "client": client,
            "corev1": client.CoreV1Api(),
            "appsv1": client.AppsV1Api(),
            "customv1": client.CustomObjectsApi(),
            "namespace": KubernetesTester.get_namespace(),
        }[name]

    @classmethod
    def setup_class(cls):
        "Will setup class (initialize kubernetes objects)"
        KubernetesTester.load_configuration()
        # will this take the subclass doc?
        test_setup = yaml.safe_load(cls.__doc__)
        KubernetesTester.prepare(test_setup, KubernetesTester.get_namespace())

    @staticmethod
    def load_configuration():
        "Loads kubernetes client configuration from kubectl config or incluster."
        try:
            config.load_kube_config()
        except Exception:
            config.load_incluster_config()

    @staticmethod
    def get_namespace():
        namespace = getenv("PROJECT_NAMESPACE")
        if not namespace:
            raise ValueError(
                "Environment variable `PROJECT_NAMESPACE` needs to be set."
            )

        return namespace

    @staticmethod
    def prepare(test_setup, namespace):
        wait_for = 0
        if "create" in test_setup:
            wait_for = test_setup["create"]["wait_for"]
            KubernetesTester.create(test_setup["create"], namespace)
        elif "update" in test_setup:
            wait_for = test_setup["update"]["wait_for"]
            KubernetesTester.patch(test_setup["update"], namespace)
        elif "delete" in test_setup:
            wait_for = test_setup["delete"]["wait_for"]
            KubernetesTester.delete(test_setup["delete"], namespace)

        KubernetesTester.wait_for(wait_for)

    @staticmethod
    def wait_for(seconds):
        "Will wait for a given amount of seconds."
        time.sleep(int(seconds))

    @staticmethod
    def create(section, namespace):
        "creates a custom object from filename"
        resource = yaml.safe_load(open(section["file"]))
        name, kind, group, version = get_crd_meta(resource)

        KubernetesTester.clients("customv1").create_namespaced_custom_object(
            group, version, namespace, plural(kind), resource
        )

    @staticmethod
    def patch(section, namespace):
        "patches custom object"
        resource = yaml.safe_load(open(section["file"]))
        name, kind, group, version = get_crd_meta(resource)
        patch = jsonpatch.JsonPatch.from_string(section["patch"])
        patched = patch.apply(resource)

        KubernetesTester.clients("customv1").patch_namespaced_custom_object(
            group, version, namespace, plural(kind), name, patched
        )

    @staticmethod
    def delete(section, namespace):
        "delete custom object"
        resource = yaml.safe_load(open(section["file"]))
        name, kind, group, version = get_crd_meta(resource)
        del_options = KubernetesTester.clients("client").V1DeleteOptions()

        KubernetesTester.clients("customv1").delete_namespaced_custom_object(
            group, version, namespace, plural(kind), name, del_options
        )

    def setup_method(self):
        self.client = client
        self.corev1 = client.CoreV1Api()
        self.appsv1 = client.AppsV1Api()
        self.customv1 = client.CustomObjectsApi()
        self.namespace = KubernetesTester.get_namespace()

    def get_group_id(self, name, om_vars):
        "Obtains the group id from group name"
        headers = {"Content-Type": "application/json"}
        endpoint = self.build_om_group_byname_endpoint(name, om_vars)
        auth = self.build_auth(om_vars["user"], om_vars["public_api_key"])
        response = requests.get(endpoint, auth=auth, headers=headers)

        return response.json()["id"]

    def om_vars(self):
        group_name = "operator-tests"
        om_vars = {
            "base_url": getenv("OM_HOST").rstrip(),
            "public_api_key": getenv("OM_API_KEY").rstrip(),
            "user": getenv("OM_USER").rstrip(),
        }
        om_vars["group_id"] = self.get_group_id(group_name, om_vars)
        return om_vars

    def build_auth(self, user, api_key):
        return HTTPDigestAuth(user, api_key)

    def build_om_group_byname_endpoint(self, name, om_vars):
        return "{}/api/public/v1.0/groups/byName/{}".format(
            om_vars["base_url"], name
        )

    def build_automation_config_endpoint(self, om_vars):
        return "{}/api/public/v1.0/groups/{}/automationConfig".format(
            om_vars["base_url"], om_vars["group_id"]
        )

    def get_automation_config(self):
        om_vars = self.om_vars()
        auth = self.build_auth(om_vars["user"], om_vars["public_api_key"])
        endpoint = self.build_automation_config_endpoint(om_vars)
        headers = {"Content-Type": "application/json"}
        response = requests.get(endpoint, auth=auth, headers=headers)

        return response.json()


def get_group(doc):
    return doc["apiVersion"].split("/")[0]


def get_version(doc):
    return doc["apiVersion"].split("/")[1]


def get_kind(doc):
    return doc["kind"]


def get_name(doc):
    return doc["metadata"]["name"]


def get_crd_meta(doc):
    return get_name(doc), get_kind(doc), get_group(doc), get_version(doc)


def plural(name):
    return "{}s".format(name.lower())
