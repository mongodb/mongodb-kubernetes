from kubetester.kubetester import KubernetesTester
from kubetester.omcr import OpsManagerCR
from kubetester.omtester import OMContext


class OpsManagerBase(KubernetesTester):
    om_cr: OpsManagerCR
    om_context: OMContext

    @classmethod
    def setup_env(cls):
        """ Overriding the base setup method - by this time the public api key should be known (if OM was created) """
        if KubernetesTester.get_resource() is not None:
            om_cr = OpsManagerBase.read_om_cr()
            group_id = "0" * 24

            api_key_secret = KubernetesTester.read_secret(KubernetesTester.namespace, om_cr.api_key_secret())
            OpsManagerBase.om_context = OMContext(om_cr.base_url(), group_id, "Backing Database",
                                                  api_key_secret['user'], api_key_secret['publicApiKey'])
            OpsManagerBase.om_cr = om_cr

    @staticmethod
    def read_om_cr() -> OpsManagerCR:
        return OpsManagerCR(KubernetesTester.get_resource(), KubernetesTester.namespace)

    @staticmethod
    def om_in_running_state():
        """ Returns true if the resource in Running state, fails fast if got into Failed error.
         This allows to fail fast in case of cascade failures """
        resource = OpsManagerBase.read_om_cr()
        if resource.get_status() is None:
            return False
        phase = resource.get_om_status()['phase']

        if phase == "Failed":
            msg = resource.get_om_status()['message']
            raise AssertionError('Got into Failed phase while waiting for Running! ("{}")'.format(msg))

        is_om_running = phase == "Running"
        is_appdb_running = resource.get_appdb_status()['phase'] == "Running"

        if is_om_running and not is_appdb_running:
            raise AssertionError("Ops Manager has Running status, but AppDB has status {}".format(resource.get_appdb_status()['phase']))

        return is_om_running
