import random

import pymongo
import time

import strgen
from kubetester import kubetester
from kubetester.kubetester import KubernetesTester

TEST_DB = "test-db"
TEST_COLLECTION = "test-collection"


class MongodTester(object):
    """ MongodTester is a general abstraction to work with mongo database. It incapsulates the client created in
    the constructor. All general methods non-specific to types of mongodb topologies should reside here. """

    def __init__(self, hosts, ssl=False):
        mongodburi = build_mongodb_uri(hosts)
        options = {}
        if ssl:
            options = {
                "ssl": True,
                "ssl_ca_certs": kubetester.SSL_CA_CERT
            }
        self.client = pymongo.MongoClient(mongodburi, **options)
        # trivial check to make sure mongod is alive
        self.client.admin.command("ismaster")

    def assert_version(self, expected_version):
        assert self.client.admin.command("buildInfo")['version'] == expected_version

    def assert_data_size(self, expected_count, test_collection=TEST_COLLECTION):
        assert self.client[TEST_DB][test_collection].count() == expected_count

    def upload_random_data(self, count, test_collection=TEST_COLLECTION):
        """ Generates random json documents and uploads them to database. This data can be later checked for
        integrity """
        print("Inserting {} fake records to {}.{}".format(count, TEST_DB, test_collection))
        target = self.client[TEST_DB][test_collection]
        buf = []
        for a in range(count):
            buf.append(generate_single_json())
            if len(buf) == 10_000:
                target.insert_many(buf)
                buf.clear()
            if (a + 1) % 50_000 == 0:
                print("Inserted {} records".format(a + 1))
        # tail
        if len(buf) > 0:
            target.insert_many(buf)
        print("Data inserted")


class StandaloneTester(MongodTester):
    def __init__(self, mdb_resource_name, ssl=False):
        hosts = build_list_of_hosts(mdb_resource_name, KubernetesTester.get_namespace(), 1)
        super().__init__(hosts, ssl)


class ReplicaSetTester(MongodTester):
    def __init__(self, mdb_resource_name, replicas_count, ssl=False):
        hosts = build_list_of_hosts(mdb_resource_name, KubernetesTester.get_namespace(), replicas_count)
        super().__init__(hosts, ssl)

    def assert_replicas_count(self, replicas_count, wait_for=60, check_every=5):
        check_times = wait_for / check_every

        while (
                (self.client.primary is None
                 or len(self.client.secondaries) < replicas_count - 1)
                and check_times >= 0
        ):
            time.sleep(check_every)
            check_times -= 1

        assert self.client.primary is not None
        assert len(self.client.secondaries) == replicas_count - 1


class ShardedClusterTester(MongodTester):
    def __init__(self, mdb_resource_name, mongos_count, ssl=False):
        hosts = build_list_of_hosts(
            mdb_resource_name + "-mongos", KubernetesTester.get_namespace(), mongos_count,
            servicename="{}-svc".format(mdb_resource_name))
        super().__init__(hosts, ssl)

    def shard_collection(self, shards_pattern, shards_count, key, test_collection=TEST_COLLECTION):
        """ enables sharding and creates zones to make sure data is spread over shards.
        Assumes that the documents have field 'key' with value in [0,10] range """
        for i in range(shards_count):
            self.client.admin.command('addShardToZone', shards_pattern.format(i), zone="zone-{}".format(i))

        for i in range(shards_count):
            self.client.admin.command('updateZoneKeyRange',
                                      db_namespace(test_collection),
                                      min={key: i * (10 / shards_count)},
                                      max={key: (i + 1) * (10 / shards_count)},
                                      zone="zone-{}".format(i))

        self.client.admin.command('enableSharding', TEST_DB)
        self.client.admin.command('shardCollection', db_namespace(test_collection), key={key: 1})


    def prepare_for_shard_removal(self, shards_pattern, shards_count):
        """ We need to map all the shards to all the zones to let shard be removed (otherwise the balancer gets
        stuck as it cannot move chunks from shards being removed) """
        for i in range(shards_count):
            for j in range(shards_count):
                self.client.admin.command('addShardToZone', shards_pattern.format(i), zone="zone-{}".format(j))

    def assert_number_of_shards(self, expected_count):
        assert len(self.client.admin.command('listShards')['shards']) == expected_count


# ------------------------- Helper functions ----------------------------

def build_list_of_hosts(mdb_resource, namespace, members, servicename=None):
    if servicename is None:
        servicename = "{}-svc".format(mdb_resource)

    return [
        build_host_fqdn(hostname(mdb_resource, idx), namespace, servicename)
        for idx in range(members)
    ]


def build_host_fqdn(hostname, namespace, servicename):
    return "{hostname}.{servicename}.{namespace}.svc.cluster.local:27017".format(
        hostname=hostname, servicename=servicename, namespace=namespace
    )


def hostname(hostname, idx):
    return "{}-{}".format(hostname, idx)


def build_mongodb_uri(hosts):
    return "mongodb://{}".format(",".join(hosts))


def generate_single_json():
    """ Generates a json with two fields. String field contains random characters and has length 100 characters. """
    return {"description": strgen.StringGenerator("[\d\w]{100}").render(), "type": random.uniform(1, 10)}


def db_namespace(collection):
    """ https://docs.mongodb.com/manual/reference/glossary/#term-namespace """
    return "{}.{}".format(TEST_DB, collection)
