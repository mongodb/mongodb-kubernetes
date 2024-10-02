import time

import pymongo
from kubetester import MongoDB, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester
from pytest import fixture


class MigrationConnectivityTests:
    @fixture(scope="module")
    def yaml_file(self):
        raise Exception("Not implemented, should be defined in a subclass")

    @fixture(scope="module")
    def mdb_resource_name(self):
        raise Exception("Not implemented, should be defined in a subclass")

    @fixture(scope="module")
    def mongo_tester(self, mdb_resource_name: str):
        raise Exception("Not implemented, should be defined in a subclass")

    @fixture(scope="module")
    def mdb_health_checker(self, mongo_tester: MongoTester) -> MongoDBBackgroundTester:
        return MongoDBBackgroundTester(
            mongo_tester,
            allowed_sequential_failures=1,
            health_function_params={
                "attempts": 1,
                "write_concern": pymongo.WriteConcern(w="majority"),
            },
        )

    @fixture
    def mdb(self, namespace, mdb_resource_name, yaml_file, custom_mdb_version: str):
        db = MongoDB.from_yaml(
            yaml_fixture(yaml_file),
            namespace=namespace,
            name=mdb_resource_name,
        )

        db["spec"]["version"] = custom_mdb_version

        try_load(db)
        return db

    def test_create_cluster(self, mdb: MongoDB):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running)

    def test_start_health_checker(self, mdb_health_checker):
        mdb_health_checker.start()

    def test_migrate_architecture(self, mdb: MongoDB):
        mdb.trigger_architecture_migration()
        mdb.assert_abandons_phase(Phase.Running, timeout=1000)
        mdb.assert_reaches_phase(Phase.Running, timeout=1000)

    def test_mdb_healthy_throughout_change_version(self, mdb_health_checker):
        mdb_health_checker.assert_healthiness()
