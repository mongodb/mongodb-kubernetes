from __future__ import annotations

import copy
from datetime import datetime, timedelta
from typing import Dict, Optional

import yaml
from kubernetes import client


class CustomObject:
    """CustomObject is an object mapping to a Custom Resource in Kubernetes. It
    includes simple facilities to update the Custom Resource, save it and
    reload its state in a object oriented manner.

    It is meant to be used to apply changes to Custom Resources and watch their
    state as it is updated by a controller; an Operator in Kubernetes parlance.

    """

    def __init__(
        self,
        name: str,
        namespace: str,
        kind: Optional[str] = None,
        plural: Optional[str] = None,
        group: Optional[str] = None,
        version: Optional[str] = None,
        api_client: Optional[client.ApiClient] = None,
    ):
        self.name = name
        self.namespace = namespace

        if any(value is None for value in (plural, kind, group, version)):
            # It is possible to have a CustomObject where some of the initial values are set
            # to None. For instance when instantiating CustomObject from a yaml file (from_yaml).
            # In this case, we need to look for the rest of the parameters from the
            # apiextensions Kubernetes API.
            crd = get_crd_names(
                plural=plural,
                kind=kind,
                group=group,
                version=version,
                api_client=api_client,
            )
            self.kind = crd.spec.names.kind
            self.plural = crd.spec.names.plural
            self.group = crd.spec.group
            self.version = crd.spec.version
        else:
            self.kind = kind
            self.plural = plural
            self.group = group
            self.version = version

        # True if this object is backed by a Kubernetes object, this is, it has
        # been loaded or saved from/to Kubernetes API.
        self.bound = False

        # Set to True if the object needs to be updated every time one of its
        # attributes is changed.
        self.auto_save = False

        # Set `auto_reload` to `True` if it needs to be reloaded before every
        # read of an attribute. This considers the `auto_reload_period`
        # attribute at the same time.
        self.auto_reload = False

        # If `auto_reload` is set, it will not reload if less time than
        # `auto_reload_period` has passed since last read.
        self.auto_reload_period = timedelta(seconds=2)

        # Last time this object was updated
        self.last_update: datetime = None

        # Sets the API used for this particular type of object
        self.api = client.CustomObjectsApi(api_client=api_client)

        if not hasattr(self, "backing_obj"):
            self.backing_obj = {
                "metadata": {"name": name, "namespace": namespace},
                "kind": self.kind,
                "apiVersion": "/".join(filter(None, [group, version])),
                "spec": {},
                "status": {},
            }

    def load(self) -> CustomObject:
        """Loads this object from the API."""

        obj = self.api.get_namespaced_custom_object(self.group, self.version, self.namespace, self.plural, self.name)

        self.backing_obj = obj
        self.bound = True

        self._register_updated()
        return self

    def create(self) -> CustomObject:
        """Creates this object in Kubernetes."""
        obj = self.api.create_namespaced_custom_object(
            self.group, self.version, self.namespace, self.plural, self.backing_obj
        )

        self.backing_obj = obj
        self.bound = True

        self._register_updated()
        return self

    def update(self) -> CustomObject:
        """Updates the object in Kubernetes. Deleting keys is done by setting them to None"""
        return create_or_update(self)

    def patch(self) -> CustomObject:
        """Patch the object in Kubernetes. Deleting keys is done by setting them to None"""
        obj = self.api.patch_namespaced_custom_object(
            self.group,
            self.version,
            self.namespace,
            self.plural,
            self.name,
            self.backing_obj,
        )
        self.backing_obj = obj

        self._register_updated()

        return obj

    def _register_updated(self):
        """Register the last time the object was updated from Kubernetes."""
        self.last_update = datetime.now()

    def _reload_if_needed(self):
        """Reloads the object is `self.auto_reload` is set to `True` and more than
        `self.auto_reload_period` time has passed since last reload."""
        if not self.auto_reload:
            return

        if self.last_update is None:
            self.reload()

        if datetime.now() - self.last_update > self.auto_reload_period:
            self.reload()

    @classmethod
    def from_yaml(cls, yaml_file, name=None, namespace=None):
        """Creates a `CustomObject` from a yaml file. In this case, `name` and
        `namespace` are optional in this function's signature, because they
        might be passed as part of the `yaml_file` document.
        """
        doc = yaml.safe_load(open(yaml_file))

        if "metadata" not in doc:
            doc["metadata"] = dict()

        if (name is None or name == "") and "name" not in doc["metadata"]:
            raise ValueError(
                "`name` needs to be passed as part of the function call "
                "or exist in the `metadata` section of the yaml document."
            )

        if (namespace is None or namespace == "") and "namespace" not in doc["metadata"]:
            raise ValueError(
                "`namespace` needs to be passed as part of the function call "
                "or exist in the `metadata` section of the yaml document."
            )

        if name is None:
            name = doc["metadata"]["name"]
        else:
            doc["metadata"]["name"] = name

        if namespace is None:
            namespace = doc["metadata"]["namespace"]
        else:
            doc["metadata"]["namespace"] = namespace

        kind = doc["kind"]
        api_version = doc["apiVersion"]
        if "/" in api_version:
            group, version = api_version.split("/")
        else:
            group = None
            version = api_version

        if getattr(cls, "object_names_initialized", False):
            obj = cls(name, namespace)
        else:
            obj = cls(name, namespace, kind=kind, group=group, version=version)

        obj.backing_obj = doc

        return obj

    @classmethod
    def define(
        cls: CustomObject,
        name: str,
        kind: Optional[str] = None,
        plural: Optional[str] = None,
        group: Optional[str] = None,
        version: Optional[str] = None,
        api_client: Optional[client.ApiClient] = None,
    ):
        """Defines a new class that will hold a particular type of object.

        This is meant to be used as a quick replacement for
        CustomObject if needed, but not extensive control or behaviour
        needs to be implemented. If your particular use case requires more
        control or more complex behaviour on top of the CustomObject class,
        consider subclassing it.
        """

        def __init__(self, name, namespace, **kwargs):
            CustomObject.__init__(
                self,
                name,
                namespace,
                kind=kind,
                plural=plural,
                group=group,
                version=version,
                api_client=api_client,
            )

        def __repr__(self):
            return "{klass_name}({name}, {namespace})".format(
                klass_name=name,
                name=repr(self.name),
                namespace=repr(self.namespace),
            )

        return type(
            name,
            (CustomObject,),
            {
                "object_names_initialized": True,
                "__init__": __init__,
                "__repr__": __repr__,
            },
        )

    def delete(self):
        """Deletes the object from Kubernetes."""
        body = client.V1DeleteOptions()

        self.api.delete_namespaced_custom_object(
            self.group, self.version, self.namespace, self.plural, self.name, body=body
        )

        self._register_updated()

    def reload(self):
        """Reloads the object from the Kubernetes API."""
        return self.load()

    def __getitem__(self, key):
        self._reload_if_needed()

        return self.backing_obj[key]

    def __contains__(self, key):
        self._reload_if_needed()
        return key in self.backing_obj

    def __setitem__(self, key, val):
        self.backing_obj[key] = val

        if self.bound and self.auto_save:
            self.update()


def get_crd_names(
    plural: Optional[str] = None,
    kind: Optional[str] = None,
    group: Optional[str] = None,
    version: Optional[str] = None,
    api_client: Optional[client.ApiClient] = None,
) -> Optional[Dict]:
    """Gets the CRD entry that matches all the parameters passed."""
    #
    # TODO: Update to `client.ApiextensionsV1Api()`
    #
    api = client.ApiextensionsV1beta1Api(api_client=api_client)

    if plural == kind == group == version is None:
        return None

    crds = api.list_custom_resource_definition()
    for crd in crds.items:
        found = True
        if group != "":
            if crd.spec.group != group:
                found = False

        if version != "":
            if crd.spec.version != version:
                found = False

        if kind is not None:
            if crd.spec.names.kind != kind:
                found = False

        if plural is not None:
            if crd.spec.names.plural != plural:
                found = False

        if found:
            return crd


def create_or_update(resource: CustomObject) -> CustomObject:
    """
    Tries to create the resource. If resource already exists (resulting in 409 Conflict),
    then it updates it instead. If the resource has been modified externally (operator)
    we try to do a client-side merge/override
    """
    tries = 0
    if not resource.bound:
        try:
            resource.create()
        except client.ApiException as e:
            if e.status != 409:
                raise e
            resource.patch()
    else:
        while tries < 10:
            if tries > 0:  # The first try we don't need to do client-side merge apply
                # do a client-side-apply
                new_back_obj_to_apply = copy.deepcopy(resource.backing_obj)  # resource and changes we want to apply

                resource.load()  # resource from the server overwrites resource.backing_obj

                # Merge annotations, and labels.
                # Client resource takes precedence
                # Spec from the given resource is taken,
                # since the operator is not supposed to do changes to the spec.
                # There can be cases where the obj from the server does not contain annotations/labels, but the object
                # we want to apply has them. But that is highly unlikely, and we can add that code in case that happens.
                resource["spec"] = new_back_obj_to_apply["spec"]
                if "metadata" in resource and "annotations" in resource["metadata"]:
                    resource["metadata"]["annotations"].update(new_back_obj_to_apply["metadata"]["annotations"])
                if "metadata" in resource and "labels" in resource["metadata"]:
                    resource["metadata"]["labels"].update(new_back_obj_to_apply["metadata"]["labels"])
            try:
                resource.patch()
                break
            except client.ApiException as e:
                if e.status != 409:
                    raise e
                print(
                    "detected a resource conflict. That means the operator applied a change "
                    "to the same resource we are trying to change"
                    "Applying a client-side merge!"
                )
                tries += 1
                if tries == 10:
                    raise Exception("Tried client side merge 10 times and did not succeed")

    return resource
