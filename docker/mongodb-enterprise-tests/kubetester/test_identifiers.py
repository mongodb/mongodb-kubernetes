import os
from typing import Any

import yaml
from kubetester.kubetester import running_locally

test_identifiers: dict = None


def set_test_identifier(identifier: str, value: Any) -> Any:
    """
    Persists random test identifier for subsequent local runs.

    Useful for creating resources with random names, e.g., S3 buckets.
    When the identifier exists, it disregards passed value and returns saved one.
    Appends identifier to .test_identifier file in working directory.
    """
    global test_identifiers

    if not running_locally():
        return value

    # this check is for in-memory cache, if the value already exists, we're trying to set value again for the same key
    if test_identifiers is not None and identifier in test_identifiers:
        raise Exception(
            f"cannot override {identifier} test identifier, existing value: {test_identifiers[identifier]}, new value: {value}."
            f"There is a high chance this function is called multiple times "
            f"in the same test case with the same bucket-name/identifier."
            f"The solution is to find the method calls and give each bucket a unique name."
        )

    # test_identifiers is an in-memory cache/global that makes
    # sure we don't generate multiple bucket names for the same test bucket
    if test_identifiers is None:
        test_identifiers = dict()

    test_identifiers_local = dict()
    test_identifiers_file = ".test_identifiers"
    if os.path.exists(test_identifiers_file):
        with open("%s" % test_identifiers_file) as f:
            test_identifiers_local = yaml.safe_load(f)

    # We have found the bucket in the cache. Let's re-use it and save it to the in-memory cache.
    if identifier in test_identifiers_local:
        test_identifiers[identifier] = test_identifiers_local[identifier]
    else:
        # The bucket is not in the cache. Let's save it in-memory and to the file cache.
        test_identifiers[identifier] = value
        test_identifiers_local[identifier] = value
        with open("%s" % test_identifiers_file, "w") as f:
            yaml.dump(test_identifiers_local, f)

    return test_identifiers[identifier]
