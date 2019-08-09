import pytest
import requests
from kubetester.kubetester import build_auth

from .kubetester import get_env_var_or_fail


def running_cloud_manager():
    "Determines if the current test is running against Cloud Manager"
    return get_env_var_or_fail("OM_HOST") == "https://cloud-qa.mongodb.com"


skip_if_cloud_manager = pytest.mark.skipif(running_cloud_manager(), reason="Do not run in Cloud Manager")


# todo use @dataclass annotation https://www.python.org/dev/peps/pep-0557/
class OMContext(object):
    def __init__(self, base_url, group_id, group_name, user, public_key, org_id=None):
        self.base_url = base_url
        self.group_id = group_id
        self.group_name = group_name
        self.user = user
        self.public_key = public_key
        self.org_id = org_id


class OMTester(object):
    """ OMTester is designed to encapsulate communication with Ops Manager. It also provides the
    set of assertion methods helping to write tests"""

    def __init__(self, om_context: OMContext):
        self.om_context = om_context

    def assert_healthiness(self):
        endpoint = self.om_context.base_url + "/monitor/health"
        print("\nChecking Ops Manager resource readiness:", endpoint)
        response = requests.request("get", endpoint)
        assert response.status_code == 200

    def assert_test_service(self):
        endpoint = self.om_context.base_url + "/test/utils/systemTime"
        response = requests.request("get", endpoint)
        assert response.status_code == 200

    def assert_support_page_enabled(self):
        """The method ends successfully if 'mms.helpAndSupportPage.enabled' is set to 'true'. It's 'false' by default.
            See mms SupportResource.supportLoggedOut()"""
        endpoint = self.om_context.base_url + "/support"
        response = requests.request("get", endpoint, allow_redirects=False)

        # logic: if mms.helpAndSupportPage.enabled==true - then status is 307, otherwise 303"
        assert response.status_code == 307

    def assert_group_exists(self):
        path = "/api/public/v1.0/groups/" + self.om_context.group_id
        response = self.om_request("get", path)

        assert response.status_code == 200

    def om_request(self, method, path, json_object=None):
        headers = {"Content-Type": "application/json"}
        auth = build_auth(self.om_context.user, self.om_context.public_key)

        endpoint = self.om_context.base_url + path
        response = requests.request(method, endpoint, auth=auth, headers=headers, json=json_object)

        if response.status_code >= 300:
            raise Exception(
                "Error sending request to Ops Manager API. {} ({}).\n Request details: {} {} (data: {})".format(
                    response.status_code, response.text, method, endpoint, json_object
                )
            )

        return response


# TODO can we move below methods to some other place?


def get_agent_cert_names(namespace):
    agent_names = ['mms-automation-agent', 'mms-backup-agent', 'mms-monitoring-agent']
    return ['{}.{}'.format(agent_name, namespace) for agent_name in agent_names]


def get_rs_cert_names(mdb_resource, namespace, *, members=3, with_internal_auth_certs=False, with_agent_certs=False):
    cert_names = [f"{mdb_resource}-{i}.{namespace}" for i in range(members)]

    if with_internal_auth_certs:
        cert_names += [f"{mdb_resource}-{i}-clusterfile.{namespace}" for i in range(members)]

    if with_agent_certs:
        cert_names += get_agent_cert_names(namespace)

    return cert_names


def get_sc_cert_names(
        mdb_resource,
        namespace,
        *,
        num_shards=1,
        members=3,
        config_members=3,
        num_mongos=2,
        with_internal_auth_certs=False,
        with_agent_certs=False
):
    names = []

    for shard_num in range(num_shards):
        for member in range(members):
            # e.g. test-tls-x509-sc-0-1.developer14
            names.append('{}-{}-{}.{}'.format(mdb_resource, shard_num, member, namespace))
            if with_internal_auth_certs:
                # e.g. test-tls-x509-sc-0-2-clusterfile.developer14
                names.append('{}-{}-{}-clusterfile.{}'.format(mdb_resource, shard_num, member, namespace))

    for member in range(config_members):
        # e.g. test-tls-x509-sc-config-1.developer14
        names.append('{}-config-{}.{}'.format(mdb_resource, member, namespace))
        if with_internal_auth_certs:
            # e.g. test-tls-x509-sc-config-1-clusterfile.developer14
            names.append('{}-config-{}-clusterfile.{}'.format(mdb_resource, member, namespace))

    for mongos in range(num_mongos):
        # e.g.test-tls-x509-sc-mongos-1.developer14
        names.append('{}-mongos-{}.{}'.format(mdb_resource, mongos, namespace))
        if with_internal_auth_certs:
            # e.g. test-tls-x509-sc-mongos-0-clusterfile.developer14
            names.append('{}-mongos-{}-clusterfile.{}'.format(mdb_resource, mongos, namespace))

    if with_agent_certs:
        names.extend(get_agent_cert_names(namespace))

    return names
