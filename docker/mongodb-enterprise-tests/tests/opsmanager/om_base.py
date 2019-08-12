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
         Note, that this method is useful ONLY if the docstring annotation was used - custom tests won't benefit """
        try:
            if KubernetesTester.get_resource() is not None:
                om_cr = OpsManagerBase.read_om_cr()
                OpsManagerBase.init_om_context(om_cr)
        except ApiException:
            # the object doesn't exist after we remove it - this is ok.
            # TODO ideally we need to distinguish API errors as we do in go code
            pass

    @staticmethod
    def read_om_cr() -> OpsManagerCR:
        return OpsManagerCR(KubernetesTester.get_resource(), KubernetesTester.get_namespace())

    @staticmethod
    def om_cr_from_resource(resource) -> OpsManagerCR:
        return OpsManagerCR(resource, KubernetesTester.get_namespace())

    @staticmethod
    def init_om_context(om_cr: OpsManagerCR):
        """ Creates OM context by Ops Manager Custom Resource. Must be called only after the CR is pushed
        and the first used is created in OM (so the API secret exists) """

        # This is the magic id of the "backing database" group in Ops Manager - the one which is used to manage the
        # appdb - it's created during OM initialization
        group_id = "0" * 24
        api_key_secret = KubernetesTester.read_secret(KubernetesTester.get_namespace(), om_cr.api_key_secret())
        OpsManagerBase.om_context = OMContext(om_cr.base_url(), group_id, "Backing Database",
                                              api_key_secret['user'], api_key_secret['publicApiKey'])
        OpsManagerBase.om_cr = om_cr

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
            raise AssertionError(
                "Ops Manager has Running status, but AppDB has status {}".format(resource.get_appdb_status()['phase']))

        return is_om_running

    @staticmethod
    def om_in_error_state():
        """ Returns true if the resource in Error state """
        resource = OpsManagerBase.read_om_cr()
        if resource.get_status() is None:
            return False
        phase = resource.get_om_status()['phase']

        if phase == "Running":
            raise AssertionError('Got into Running phase while waiting for Error!')

        return phase == "Failed"

    @staticmethod
    def om_is_deleted():
        """ Returns true if the resource has been removed """
        try:
            KubernetesTester.get_resource()
            return False
        except ApiException:
            return True

