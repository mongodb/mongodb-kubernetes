import pytest

from kubetester import KubernetesTester

from operator import attrgetter


class TestStandaloneSchemaCredentialsMissing(KubernetesTester):
    """
    name: Validation for standalone (credentials missing)
    create:
      file: fixtures/standalone.yaml
      patch: '[{"op":"remove","path":"/spec/credentials"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True

class TestStandaloneSchemaProjectMissing(KubernetesTester):
    """
    name: Validation for standalone (project missing)
    create:
      file: fixtures/standalone.yaml
      patch: '[{"op":"remove","path":"/spec/project"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True

class TestStandaloneSchemaVersionMissing(KubernetesTester):
    """
    name: Validation for standalone (version missing)
    create:
      file: fixtures/standalone.yaml
      patch: '[{"op":"remove","path":"/spec/version"}]'
      exception: 'Unprocessable Entity'
    """

    def test_validation_ok(self):
        assert True
