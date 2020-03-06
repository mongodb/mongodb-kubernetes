import json
import os
import re
from typing import Dict

from kubernetes.client.rest import ApiException
from kubetester.kubetester import KubernetesTester
from kubetester.omcr import OpsManagerCR
from kubetester.omtester import OMContext


class OpsManagerBase(KubernetesTester):
    om_cr: OpsManagerCR
    om_context: OMContext

    @classmethod
    def setup_env(cls):
        """ Preparing OM context and OM custom resource - by this time the public api key should be known (if OM was created).
         Note, that this method is useful ONLY if the docstring annotation was used - custom tests won't benefit
          TODO remove this and use the MongodbOpsManager object, it's terrible... """
        try:
            if (
                hasattr(KubernetesTester, "namespace")
                and KubernetesTester.get_resource() is not None
            ):
                OpsManagerBase.om_cr = OpsManagerBase.read_om_cr()
                OpsManagerBase.init_om_context(OpsManagerBase.om_cr)
        except ApiException:
            # the object doesn't exist after we remove it - this is ok.
            # TODO ideally we need to distinguish API errors as we do in go code
            pass

    @staticmethod
    def read_om_cr() -> OpsManagerCR:
        return OpsManagerCR(
            KubernetesTester.get_resource(), KubernetesTester.get_namespace()
        )

    @staticmethod
    def om_cr_from_resource(resource) -> OpsManagerCR:
        return OpsManagerCR(resource, KubernetesTester.get_namespace())

    @staticmethod
    def init_om_context(om_cr: OpsManagerCR):
        """ Creates OM context by Ops Manager Custom Resource. Must be called only after the CR is pushed
        and the first user is created in OM (so the API secret exists) """

        api_key_secret = KubernetesTester.read_secret(
            KubernetesTester.get_namespace(), om_cr.api_key_secret()
        )
        OpsManagerBase.om_context = OMContext(
            om_cr.base_url(), api_key_secret["user"], api_key_secret["publicApiKey"],
        )
        OpsManagerBase.om_cr = om_cr

    @staticmethod
    def om_in_running_state():
        return OpsManagerBase.om_in_desired_state("Running")

    @staticmethod
    def om_in_desired_state(state: str, message: str = None):
        """ Returns true if the resource in desired state, fails fast if got into Failed error.
         This allows to fail fast in case of cascade failures """
        resource = OpsManagerBase.read_om_cr()
        if resource.get_status() is None:
            return False
        phase = resource.get_om_status()["phase"]

        if phase == "Failed":
            msg = resource.get_om_status()["message"]
            raise AssertionError(
                'Got into Failed phase while waiting for Running! ("{}")'.format(msg)
            )

        is_om_in_desired_state = phase == state
        if message is not None:
            regexp = re.compile(message)
            is_om_in_desired_state = is_om_in_desired_state and regexp.match(
                resource.get_om_status()["message"]
            )

        is_appdb_running = resource.get_appdb_status()["phase"] == "Running"

        if is_om_in_desired_state and not is_appdb_running:
            raise AssertionError(
                "Ops Manager has {} status, but AppDB has status {}".format(
                    state, resource.get_appdb_status()["phase"]
                )
            )

        return is_om_in_desired_state

    @staticmethod
    def om_in_error_state():
        """ Returns true if the resource in Error state """
        resource = OpsManagerBase.read_om_cr()
        if resource.get_status() is None:
            return False
        phase = resource.get_om_status()["phase"]

        if phase == "Running":
            raise AssertionError("Got into Running phase while waiting for Error!")

        return phase == "Failed"

    @staticmethod
    def om_is_deleted():
        """ Returns true if the resource has been removed.
        TODO this and other methods using static methods from 'KubernetesTester' won't work if 2 different resources
        are created (OM + MDB for example)"""
        try:
            KubernetesTester.get_resource()
            return False
        except ApiException:
            return True

    @staticmethod
    def appdb_in_running_state():
        """ Returns true if the AppDB in Running state, fails fast if got into Failed error
         This allows to fail fast in case of cascade failures """
        resource = OpsManagerBase.read_om_cr()
        if resource.get_status() is None:
            return False
        phase = resource.get_appdb_status()["phase"]

        if phase == "Failed":
            msg = resource.get_appdb_status()["message"]
            raise AssertionError(
                'AppDB got into Failed phase while waiting for Running! ("{}")'.format(
                    msg
                )
            )

        return resource.get_appdb_status()["phase"] == "Running"

    @staticmethod
    def get_bundled_appdb_version() -> str:
        version = os.getenv("BUNDLED_APP_DB_VERSION", None)
        if version is None:
            raise ValueError("BUNDLED_APP_DB_VERSION needs to be defined")
        return version.partition("-")[0]

    def get_appdb_automation_config(self) -> Dict:
        cm = KubernetesTester.read_configmap(
            KubernetesTester.get_namespace(),
            "{}-config".format(self.om_cr.app_db_name()),
        )
        automation_config_str = cm["cluster-config.json"]
        return json.loads(automation_config_str)

    def get_appdb_password(self) -> str:
        secret = self.read_secret(
            self.get_namespace(), "{}-password".format(self.om_cr.app_db_name())
        )
        return secret["password"]
