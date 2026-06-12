import unittest

from kubetester.failure_fingerprint import failure_category, failure_fingerprint


class TestFailureFingerprint(unittest.TestCase):
    def test_masks_resource_name_and_timeout(self):
        a = failure_fingerprint(
            "Timeout (300) reached while waiting for MongoDB (mdb-rs)| status: Phase.Pending| message: StatefulSet not ready"
        )
        b = failure_fingerprint(
            "Timeout (900) reached while waiting for MongoDB (sharded-cluster-tls-scram-sha-256)| status: Phase.Pending| message: StatefulSet not ready"
        )
        self.assertEqual(a, b)
        self.assertIn("Timeout (<n>)", a)
        self.assertIn("MongoDB (<name>)", a)

    def test_collapses_repeated_statefulset_not_ready(self):
        one = failure_fingerprint(
            "Timeout (700) reached while waiting for MongoDB (multi-replica-set)| status: Phase.Pending| message: StatefulSet not ready"
        )
        many = failure_fingerprint(
            "Timeout (700) reached while waiting for MongoDB (multi-replica-set)| status: Phase.Pending| message: StatefulSet not ready, StatefulSet not ready, StatefulSet not ready"
        )
        self.assertEqual(one, many)

    def test_agents_goal_state_collapses_across_process_lists(self):
        a = failure_fingerprint(
            "Timeout (1000) reached while waiting for MongoDB (sh001-single)| status: Phase.Failed| message: Failed to create/update (Ops Manager reconciliation phase): automation agents haven't reached READY state during defined interval: MongoDB agents haven't reached READY state; 2 processes waiting to reach automation config goal state (version=1): [sh001-single-0-0@-1 sh001-single-mongos-0@-1], 1 processes reached goal state: [sh001-single-config-0]."
        )
        b = failure_fingerprint(
            "Timeout (900) reached while waiting for MongoDB (mdb-sh)| status: Phase.Failed| message: Failed to create/update (Ops Manager reconciliation phase): automation agents haven't reached READY state during defined interval: MongoDB agents haven't reached READY state; 5 processes waiting to reach automation config goal state (version=2): [mdb-sh-0-0@-1 mdb-sh-0-1@-1 mdb-sh-0-2@-1 mdb-sh-mongos-0@-1 mdb-sh-mongos-1@-1], 3 processes reached goal state: [mdb-sh-config-0 mdb-sh-config-1 mdb-sh-config-2]."
        )
        self.assertEqual(a, b)

    def test_masks_org_id_and_project_name(self):
        a = failure_fingerprint(
            'Got into Failed phase while waiting for Running! ("Error reading or creating project in Ops Manager: organization with id 6419caa39a37362fa6d3cb6d not found: Status: 401 (Unauthorized), Detail: You are not authorized for this resource.")'
        )
        b = failure_fingerprint(
            'Got into Failed phase while waiting for Running! ("Error reading or creating project in Ops Manager: organization with id abcdef012345678901234567 not found: Status: 401 (Unauthorized), Detail: You are not authorized for this resource.")'
        )
        self.assertEqual(a, b)
        self.assertIn("<id>", a)

    def test_keeps_distinct_http_status_codes(self):
        # 401 vs 400 vs 409 are different failure classes and must NOT merge.
        s401 = failure_fingerprint(
            'Got into Failed phase while waiting for Running! ("Failed to create/update (Ops Manager reconciliation phase): Status: 401 (Unauthorized), Detail: You are not authorized for this resource.")'
        )
        s400 = failure_fingerprint(
            'Got into Failed phase while waiting for Running! ("Failed to create/update (Ops Manager reconciliation phase): Status: 400 (Bad Request), Detail: Invalid config: Cannot add/remove multiple voting members of a replica set at once")'
        )
        self.assertNotEqual(s401, s400)

    def test_keeps_distinct_401_operations(self):
        # The leading clause says which operation hit the 401 - diagnostically meaningful, keep separate.
        agent = failure_fingerprint(
            'Got into Failed phase while waiting for Running! ("Failed to get agent auth mode: Status: 401 (Unauthorized), Detail: You are not authorized for this resource.")'
        )
        project = failure_fingerprint(
            'Got into Failed phase while waiting for Running! ("Error reading or creating project in Ops Manager: organization with id 6419caa39a37362fa6d3cb6d not found: Status: 401 (Unauthorized), Detail: You are not authorized for this resource.")'
        )
        self.assertNotEqual(agent, project)

    def test_category_infra_for_401(self):
        fp = failure_fingerprint(
            'Got into Failed phase while waiting for Running! ("Status: 401 (Unauthorized), Detail: You are not authorized for this resource.")'
        )
        self.assertEqual(failure_category(fp), "infra")

    def test_category_infra_for_etcd_and_conflict(self):
        etcd = failure_fingerprint(
            'Got into Failed phase while waiting for Running! ("Failed to write deployment state after updating status: etcdserver: request timed out")'
        )
        conflict = failure_fingerprint(
            'Got into Failed phase while waiting for Running! ("Failed to create/update (Ops Manager reconciliation phase): Status: 409 (Conflict), ErrorCode: AUTOMATION_CONFIG_CONCURRENT_MODIFICATION, Detail: Another session or user has already published changes.")'
        )
        self.assertEqual(failure_category(etcd), "infra")
        self.assertEqual(failure_category(conflict), "infra")

    def test_category_not_ready_for_statefulset_pending(self):
        fp = failure_fingerprint(
            "Timeout (300) reached while waiting for MongoDB (mdb-rs)| status: Phase.Pending| message: StatefulSet not ready"
        )
        self.assertEqual(failure_category(fp), "not_ready")

    def test_category_not_ready_for_replicaset_pending(self):
        # Community resource wording for the same "pods not up" condition.
        fp = failure_fingerprint(
            "Timeout (300) reached while waiting for MongoDB (mdbc-rs)| status: Phase.Pending| message: ReplicaSet is not yet ready, retrying in 10 seconds"
        )
        self.assertEqual(failure_category(fp), "not_ready")

    def test_category_agents_not_ready(self):
        fp = failure_fingerprint(
            "Timeout (1000) reached while waiting for MongoDB (sh001-single)| status: Phase.Failed| message: Failed to create/update (Ops Manager reconciliation phase): automation agents haven't reached READY state during defined interval: MongoDB agents haven't reached READY state; 2 processes waiting to reach automation config goal state (version=1): [sh001-single-0-0@-1 sh001-single-mongos-0@-1], 1 processes reached goal state: [sh001-single-config-0]."
        )
        self.assertEqual(failure_category(fp), "agents_not_ready")

    def test_category_spec_invalid_is_not_a_flake(self):
        # Deterministic bad-spec failures must be separable from flakes.
        fp = failure_fingerprint(
            'Got into Failed phase while waiting for Running! ("Failed to create/update (Ops Manager reconciliation phase): Status: 400 (Bad Request), Detail: Invalid config: Cannot add/remove multiple voting members of a replica set at once")'
        )
        self.assertEqual(failure_category(fp), "spec_invalid")

    def test_category_unknown_for_novel_message(self):
        fp = failure_fingerprint("Something nobody has ever seen before happened")
        self.assertEqual(failure_category(fp), "unknown")

    def test_fingerprint_is_idempotent(self):
        msg = "Timeout (300) reached while waiting for MongoDB (mdb-rs)| status: Phase.Pending| message: StatefulSet not ready"
        once = failure_fingerprint(msg)
        twice = failure_fingerprint(once)
        self.assertEqual(once, twice)

    def test_handles_none_and_empty(self):
        self.assertEqual(failure_fingerprint(None), "")
        self.assertEqual(failure_fingerprint(""), "")
        self.assertEqual(failure_category(failure_fingerprint(None)), "unknown")


if __name__ == "__main__":
    unittest.main()
