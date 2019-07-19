import pytest

from .kubetester import get_env_var_or_fail


def running_cloud_manager():
    "Determines if the current test is running against Cloud Manager"
    return get_env_var_or_fail("OM_HOST") == "https://cloud-qa.mongodb.com"


skip_if_cloud_manager = pytest.mark.skipif(running_cloud_manager(), reason="Do not run in Cloud Manager")


class OMTester(object):
    """ OMTester is designed to """
    pass


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
