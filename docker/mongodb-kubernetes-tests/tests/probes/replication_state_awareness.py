"""
replication_state_awareness tests that the agent on each Pod is aware of
replication_state and that the entry is writen to '{statuses[0].ReplicaStatus}'
in the file /var/log/mongodb-mms-automation/agent-health-status.json
"""

import asyncio
import functools
import logging
import random
import string
import time
from typing import Callable, Dict, List

import pymongo
import yaml
from kubernetes.client.rest import ApiException
from kubetester import find_fixture, wait_until
from kubetester.mongodb import MongoDB
from kubetester.mongodb_utils_replicaset import generic_replicaset
from kubetester.mongotester import upload_random_data
from kubetester.phase import Phase
from pytest import fixture, mark


def large_json_generator() -> Callable[[], Dict]:
    """
    Returns a function that generates a dictionary with a random attribute.
    """
    _doc = yaml.safe_load(open(find_fixture("deployment_tls.json")))
    rand_generator = random.SystemRandom()

    def inner() -> Dict:
        doc = _doc.copy()
        random_id = "".join(rand_generator.choice(string.ascii_uppercase + string.digits) for _ in range(30))
        doc["json_generator_id"] = random_id

        return doc

    return inner


async def upload_random_data_async(client: pymongo.MongoClient, task_name: str = None, count: int = 50_000):
    fn = functools.partial(
        upload_random_data,
        client=client,
        generation_function=large_json_generator(),
        count=count,
        task_name=task_name,
    )
    return await asyncio.get_event_loop().run_in_executor(None, fn)


def create_writing_task(client: pymongo.MongoClient, name: str, count: int) -> asyncio.Task:
    """
    Creates an async Task that uploads documents to a MongoDB database.
    Async tasks in Python start right away after they are created.
    """
    return asyncio.create_task(upload_random_data_async(client, name, count))


def create_writing_tasks(client: pymongo.MongoClient, prefix: str, task_sizes: List[int] = None) -> asyncio:
    """
    Creates many async tasks to upload documents to a MongoDB database.
    """
    return [create_writing_task(client, prefix + str(task), task) for task in task_sizes]


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    rs = generic_replicaset(namespace, version="4.4.2")

    rs["spec"]["persistent"] = True
    return rs.create()


@mark.e2e_replication_state_awareness
def test_replicaset_reaches_running_state(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_replication_state_awareness
def test_inserts_50k_documents(replica_set: MongoDB):
    client = replica_set.tester().client

    upload_random_data(
        client,
        50_000,
        generation_function=large_json_generator(),
        task_name="uploader_50k",
    )


@mark.e2e_replication_state_awareness
@mark.asyncio
async def test_fill_up_database(replica_set: MongoDB):
    """
    Writes 1 million documents to the database.
    """
    client: pymongo.MongoClient = replica_set.tester().client

    tasks = create_writing_tasks(client, "uploader_", [500_000, 400_000, 100_000])
    for task in tasks:
        await task

    logging.info("All uploaders have finished.")


@mark.e2e_replication_state_awareness
def test_kill_pod_while_writing(replica_set: MongoDB):
    """Keeps writing documents to the database while it is being
    restarted."""
    logging.info("Restarting StatefulSet holding the MongoDBs: sts/{}".format(replica_set.name))
    replica_set["spec"]["podSpec"] = {
        "podTemplate": {
            "spec": {
                "containers": [
                    {
                        "name": "mongodb-enterprise-database",
                        "resources": {"limits": {"cpu": "2", "memory": "2Gi"}},
                    }
                ]
            }
        }
    }
    current_gen = replica_set.get_generation()
    replica_set.update()
    wait_until(lambda: replica_set.get_generation() >= current_gen)

    replica_set.assert_reaches_phase(Phase.Running, timeout=800)
