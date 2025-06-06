from typing import Dict

import pytest
from kubernetes.client import ApiException
from kubetester import try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture


class BackupSnapshotScheduleTests:
    """Test executes snapshot schedule tests on top of existing ops_manager.

    This test class is intended to be reused by inheriting from it and overriding fixtures:
     mdb - for providing base MongoDB resource
     mdb_version - for customizing MongoDB versions.
     om_project_name - for customizing project name - for multiple tests running on top of single OM.
    """

    @fixture
    def mdb(self, ops_manager: MongoDBOpsManager):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=ops_manager.namespace,
            name="mdb-backup-snapshot-schedule",
        )

        try_load(resource)
        return resource

    @fixture
    def mdb_version(self, custom_mdb_version):
        return custom_mdb_version

    @fixture
    def om_project_name(self):
        return "backupSnapshotSchedule"

    def test_create_mdb_with_backup_enabled_and_configured_snapshot_schedule(
        self,
        mdb: MongoDB,
        ops_manager: MongoDBOpsManager,
        mdb_version: str,
        om_project_name: str,
    ):
        mdb.configure(ops_manager, om_project_name)

        mdb["spec"]["version"] = ensure_ent_version(mdb_version)
        mdb.configure_backup(mode="enabled")
        mdb["spec"]["backup"]["snapshotSchedule"] = {
            "fullIncrementalDayOfWeek": "MONDAY",
        }
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=1000)
        mdb.assert_backup_reaches_status("STARTED")

        self.assert_snapshot_schedule_in_ops_manager(
            mdb.get_om_tester(),
            {
                "fullIncrementalDayOfWeek": "MONDAY",
            },
        )

    @pytest.mark.skip(
        reason="Backup termination is not handled properly by the operator: https://jira.mongodb.org/browse/CLOUDP-149270"
    )
    def test_when_backup_terminated_snapshot_schedule_is_ignored(self, mdb: MongoDB):
        mdb.configure_backup(mode="disabled")
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running)
        mdb.assert_backup_reaches_status("STOPPED")

        mdb.load()
        mdb.configure_backup(mode="terminated")
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running)
        mdb.assert_backup_reaches_status("TERMINATING")

        try:
            mdb.get_om_tester().api_read_backup_snapshot_schedule()
            assert False, "exception about missing backup configuration should be raised"
        except Exception:
            pass

        mdb.configure_backup(mode="enabled")
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running)
        mdb.assert_backup_reaches_status("STARTED")

    def test_stop_backup_and_change_snapshot_schedule(self, mdb: MongoDB):
        mdb.configure_backup(mode="disabled")
        self.update_snapshot_schedule(
            mdb,
            {
                "fullIncrementalDayOfWeek": "WEDNESDAY",
            },
        )
        mdb.assert_backup_reaches_status("STOPPED")

        self.assert_snapshot_schedule_in_ops_manager(
            mdb.get_om_tester(),
            {
                "fullIncrementalDayOfWeek": "WEDNESDAY",
            },
        )

    def test_enable_backup_and_change_snapshot_schedule(self, mdb: MongoDB):
        mdb.configure_backup(mode="enabled")
        self.update_snapshot_schedule(
            mdb,
            {
                "fullIncrementalDayOfWeek": "TUESDAY",
            },
        )
        mdb.assert_backup_reaches_status("STARTED")

        self.assert_snapshot_schedule_in_ops_manager(
            mdb.get_om_tester(),
            {
                "fullIncrementalDayOfWeek": "TUESDAY",
            },
        )

    def test_only_one_field_is_set(self, mdb: MongoDB):
        prev_snapshot_schedule = mdb.get_om_tester().api_read_backup_snapshot_schedule()

        self.update_snapshot_schedule(
            mdb,
            {
                "fullIncrementalDayOfWeek": "THURSDAY",
            },
        )

        expected_snapshot_schedule = dict(prev_snapshot_schedule)
        expected_snapshot_schedule["fullIncrementalDayOfWeek"] = "THURSDAY"

        self.assert_snapshot_schedule_in_ops_manager(mdb.get_om_tester(), expected_snapshot_schedule)

    def test_check_all_fields_are_set(self, mdb: MongoDB):
        self.update_and_assert_snapshot_schedule(
            mdb,
            {
                "snapshotIntervalHours": 12,
                "snapshotRetentionDays": 3,
                "dailySnapshotRetentionDays": 5,
                "weeklySnapshotRetentionWeeks": 4,
                "monthlySnapshotRetentionMonths": 5,
                "pointInTimeWindowHours": 6,
                "referenceHourOfDay": 1,
                "referenceMinuteOfHour": 3,
                "fullIncrementalDayOfWeek": "THURSDAY",
            },
        )

    def test_validations(self, mdb: MongoDB):
        # we're smoke-testing if any of the CRD validations works
        try:
            self.update_snapshot_schedule(
                mdb,
                {
                    "fullIncrementalDayOfWeek": "January",
                },
            )
        except ApiException as e:
            assert e.status == 422  # "Unprocessable Entity"
            pass

        # revert state back to running
        self.update_snapshot_schedule(
            mdb,
            {
                "fullIncrementalDayOfWeek": "WEDNESDAY",
            },
        )

    @staticmethod
    def update_snapshot_schedule(mdb: MongoDB, snapshot_schedule: Dict):
        last_transition = mdb.get_status_last_transition_time()

        mdb["spec"]["backup"]["snapshotSchedule"] = snapshot_schedule
        mdb.update()

        mdb.assert_state_transition_happens(last_transition)
        mdb.assert_reaches_phase(Phase.Running, ignore_errors=True)

    @staticmethod
    def assert_snapshot_schedule_in_ops_manager(om_tester: OMTester, expected_snapshot_schedule: Dict):
        snapshot_schedule = om_tester.api_read_backup_snapshot_schedule()

        for k, v in expected_snapshot_schedule.items():
            assert k in snapshot_schedule
            assert snapshot_schedule[k] == v

    @staticmethod
    def update_and_assert_snapshot_schedule(mdb: MongoDB, snapshot_schedule: Dict):
        BackupSnapshotScheduleTests.update_snapshot_schedule(mdb, snapshot_schedule)
        BackupSnapshotScheduleTests.assert_snapshot_schedule_in_ops_manager(mdb.get_om_tester(), snapshot_schedule)
