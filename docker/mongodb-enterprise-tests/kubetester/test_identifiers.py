import os

import yaml
from typing import Any

from kubetester.kubetester import running_locally

test_identifiers: dict = None


def set_test_identifier(identifier: str, value: Any) -> Any:
    """
    Persists random test identifier for subsequent local runs.

    Useful for creating resources with random names, e.g. S3 buckets.
    When the identifier exists it disregards passed value and returns saved one.
    Appends identifier to .test_identifier file in working directory.
    """
    global test_identifiers

    if not running_locally():
        return value

    # this check is for in-memory cache, if the value already exists we're trying to set value again for the same key
    if test_identifiers is not None and identifier in test_identifiers:
        raise Exception(f"cannot override {identifier} test identifier, existing value: {test_identifiers[identifier]}, new value: {value}")

    test_identifiers_file = ".test_identifiers"
    if test_identifiers is None:
        if os.path.exists(test_identifiers_file):
            with open("%s" % test_identifiers_file) as f:
                test_identifiers = yaml.safe_load(f)
        else:
            test_identifiers = dict()

    if identifier in test_identifiers:
        return test_identifiers[identifier]

    test_identifiers[identifier] = value

    with open("%s" % test_identifiers_file, "w") as f:
        yaml.dump(test_identifiers, f)

    return test_identifiers[identifier]
