# Enterprise Kubernetes E2E Testing #

The MongoDB Enterprise Kubernetes Operator uses a declarative testing
framwork based on the Python programming language and the Pytest
module. Its design allow the developer to write tests in a
declarative way and verify the results by using a collection of helper
functions that check the state of the Kubernetes cluster.

# Quick start #

The goal of this guide is to allow you to run your tests locally as
regular Python tests. The resulting "experience" should be something
like:

``` bash
$ pytest -m e2e_replica_set
===================================================================== test session starts ===========================================================
platform linux -- Python 3.6.8, pytest-4.3.1, py-1.8.0, pluggy-0.9.0 -- /home/rvalin/.virtualenvs/operator-tests/bin/python
cachedir: .pytest_cache
rootdir: /home/rvalin/workspace/go/src/github.com/mongodb/mongodb-kubernetes/docker/mongodb-kubernetes-tests, inifile: pytest.ini
collected 168 items / 131 deselected / 37 selected
tests/replicaset/replica_set.py::TestReplicaSetCreation::test_replica_set_sts_exists PASSED                                                    [  2%]
tests/replicaset/replica_set.py::TestReplicaSetCreation::test_sts_creation PASSED                                                              [  5%]
tests/replicaset/replica_set.py::TestReplicaSetCreation::test_sts_metadata PASSED                                                              [  8%]
tests/replicaset/replica_set.py::TestReplicaSetCreation::test_sts_replicas PASSED                                                              [ 10%]
tests/replicaset/replica_set.py::TestReplicaSetCreation::test_sts_template PASSED                                                              [ 13%]

...

tests/replicaset/replica_set.py::TestReplicaSetCreation::test_replica_set_was_configured SKIPPED                                               [ 51%]
tests/replicaset/replica_set.py::TestReplicaSetUpdate::test_replica_set_sts_should_exist PASSED                                                [ 54%]
tests/replicaset/replica_set.py::TestReplicaSetUpdate::test_sts_update PASSED                                                                  [ 56%]
tests/replicaset/replica_set.py::TestReplicaSetUpdate::test_sts_metadata PASSED                                                                [ 59%]

...

tests/replicaset/replica_set.py::TestReplicaSetUpdate::test_backup PASSED                                                                      [ 94%]
tests/replicaset/replica_set.py::TestReplicaSetDelete::test_replica_set_sts_doesnt_exist PASSED                                                [ 97%]
tests/replicaset/replica_set.py::TestReplicaSetDelete::test_service_does_not_exist PASSED                                                      [100%]
============================================= 36 passed, 1 skipped, 131 deselected, 10 warnings in 89.47 seconds ====================================

```

This is a full E2E experience running locally that takes about 90
seconds.

## Configuring/Installing dependencies ##

In order to run, the local E2E tests need certain dependant
components:

* Ops Manager >= 4.0
* `kubectl` context to use configured Kubernetes Cluster
* Operator `Project` and `Credentials` created
* Kubernetes Namespace created
* Python 3.6 and dependencies

Most of the required configuration is achieved by using `make` at the
root of the project. [This
document](https://wiki.corp.mongodb.com/display/MMS/Setting+up+local+development+and+E2E+testing)
will guide you on how to do this.

The result of this is to have a running Ops Manager with
configurations saved on the `~/.operator-dev/om` file. This file will
be read by the E2E testing framework to configure itself. Once you
have done this, you can proceed to complete the Python installation.

### Installing Python and Dependencies ###

Run `scripts/dev/install.sh` or `scripts/dev/recreate_python_venv.sh` to install necessary tools and create python virtualenv.

* After the first run, when coming back to the project it should be
required to `activate` your virtual environment once again.
``` bash
source venv/bin/activate
```

There are many mechanisms to achieve this in a more automated way,
I'll leave that to each person to decide the one that suits their needs.

# Running the E2E Tests Locally #


The tests are defined in terms of "markers" in the Python source
files. To run each test, you need to specify which "marker" you want
to run, for instance:

``` bash
# Run the Replica Set Enterprise Installation E2E Test
pytest -m e2e_replica_set

# Run the TLS Upgrade E2E Test
pytest -m e2e_replica_set_tls_require_upgrade
```

These markers correspond to the name of the `task` in Evergreen. It is
handy as they can be copied over the command line to try the tests
locally, that usually run faster.

To find the list of available E2E tasks you can do the following:

    grep pytest.mark.e2e * -R | sort | uniq | cut -d "@" -f 2

The `@pytest.mark.<name>` syntax is a class annotation we use to
indicate which test classes need to be run. But for now they help us
to call a particular E2E task we are interested in.


## Building test image

```bash
make prepare-local-e2e
cd docker/mongodb-kubernetes-tests
docker buildx build --progress plain --platform linux/amd64,linux/arm64,linux/s390x,linux/ppc64le . -f Dockerfile -t "${BASE_REPO_URL}mongodb-kubernetes-tests:evergreen" \
 --build-arg PYTHON_VERSION="3.13.7"

docker push "${BASE_REPO_URL}mongodb-kubernetes-tests:evergreen"
```

# Writing New Tests #

### Create a new Python test file ###

Create a new Python file that will hold the test, the contents of this
file will be something like:

``` python
import pytest
from kubetester.kubetester import KubernetesTester
from kubernetes import client


@pytest.mark.e2e_my_new_feature
class TestMyNewFeatureShouldPass(KubernetesTester):
    """
    name: My New Feature Test 01
    create:
      file: my-mdb-object-to-test.yaml
      wait_until: in_running_state
    """

    def test_something_about_my_new_object(self):
        mdb_object_name = "my-mdb-object-to-test"
        mdb = client.CustomObjectsApi().get_namespaced_custom_object(
            "mongodb.com", "v1", self.namespace, "mongodb", mdb_object_name
        )

        assert mdb["status"]["phase"] == "Running"
```

The `my-mdb-object-to-test.yaml` will reside in one of the `fixtures`
directories and contain something like:

``` yaml
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: my-mdb-object-to-test
spec:
  version: 4.0.8
  type: Standalone
  project: my-project
  credentials: my-credentials
  persistent: false
```

Check the `@pytest.mark` decorator we have set for this test class, this is
what we'll use to run the test locally with:

``` bash
$ pytest -m e2e_my_new_feature
========================================== test session starts ===========================================
platform linux -- Python 3.6.8, pytest-4.3.1, py-1.8.0, pluggy-0.9.0 -- /home/rvalin/.virtualenvs/operator-tests/bin/python
cachedir: .pytest_cache
rootdir: /home/rvalin/workspace/go/src/github.com/mongodb/mongodb-kubernetes/docker/mongodb-kubernetes-tests, inifile: pytest.ini
collected 170 items / 169 deselected / 1 selected
tests/mixed/sample_test.py::TestMyNewFeatureShouldPass::test_something_about_my_new_object PASSED  [100%]
=============================== 1 passed, 169 deselected in 40.62 seconds ================================
```

In this run `pytest` was able to find our e2e test with the name
`e2e_my_new_feature`, the same we used in the `pytest.mark` decorator
and it run by finding our Kubernetes environment by reading `kubectl`
configuration.

### Make sure your test resources are removed! ###

There are a few handy functions to have your MDB resources removed,
the usual path to do this is to have a new function that will remove
the object for you:

``` python
def test_mdb_object_is_removed(self):
    mdb_object_name = "my-mdb-object-to-test"
    delete_opts = client.V1DeleteOptions()

    mdb = client.CustomObjectsApi().delete_namespaced_custom_object(
                "mongodb.com", "v1", self.namespace, "mongodb", mdb_object_name, body=delete_opts
    )

    assert mdb["status"] == "Success"
```

### Finally, add an entry into Evergreen ###

This is a 2-step process, which requires adding a new E2E task and add
this new task into a task group.

First you add a new task, under the `tasks` dictionary in the
`evergreen.yaml` file:

``` yaml
tasks:
- name: e2e_my_new_feature
  commands:
    - func: "e2e_test"
```

And secondly, add this task into a `task_group`. Find the one that
matches whatever you are testing and add the task in there. If your
task is testing something structural, it should go into
`e2e_core_task_group`, like:

``` yaml
- name: e2e_core_task_group
  max_hosts: 100
  setup_group:
    - func: "clone"
    - func: "setup_kubectl"
  tasks:
    - e2e_all_mongodb_resources_parallel
    - e2e_standalone_config_map
    - .... # more tasks
    - e2e_my_new_feature  # this is our new task!
```

After doing this, your task will be added into the list of tasks
executed by Evergreen on each test run.
