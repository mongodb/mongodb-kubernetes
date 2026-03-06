import pytest
from kubetester.kubetester import EXTERNALLY_MANAGED_TAG, MAX_TAG_LEN, KubernetesTester, fixture
from kubetester.omtester import should_include_tag


@pytest.mark.e2e_replica_set_groups
class TestReplicaSetOrganizationsPagination(KubernetesTester):
    """
    name: Test for configuration when organization id is not specified but the organization exists already
    description: |
      Both organization and group already exist.
      Two things are tested:
      1. organization id is not specified in config map but the organization with the same name already exists
      so the Operator will find it by name
      2. the group already exists so no new group will be created
      The test is skipped for cloud manager as we cannot create organizations there ("API_KEY_CANNOT_CREATE_ORG")
    """

    all_orgs_ids = []
    all_groups_ids = []
    org_id = None
    group_name = None

    @classmethod
    def setup_env(cls):
        # Create 5 organizations
        cls.all_orgs_ids = cls.create_organizations(5)

        # Create another organization with the same name as group
        cls.group_name = KubernetesTester.random_k8s_name("replica-set-group-test-")
        cls.org_id = cls.create_organization(cls.group_name)

        # Create 5 groups inside the organization
        cls.all_groups_ids = cls.create_groups(cls.org_id, 5)

        # Create the group manually (btw no tag will be set - this must be fixed by the Operator)
        cls.create_group(cls.org_id, cls.group_name)

        # Update the config map - change the group name, no orgId
        cls.patch_config_map(
            cls.get_namespace(),
            "my-project",
            {"projectName": cls.group_name, "orgId": ""},
        )

        print('Patched config map, now it has the projectName "{}"'.format(cls.group_name))

    def test_standalone_created_organization_found(self):
        groups_in_org = self.get_groups_in_organization_first_page(self.__class__.org_id)["totalCount"]

        # Create a replica set - both the organization and the group will be found (after traversing pages)
        self.create_custom_resource_from_file(self.get_namespace(), fixture("replica-set-single.yaml"))
        KubernetesTester.wait_until("in_running_state", 150)

        # Making sure no more groups and organizations were created, but the tag was fixed by the Operator
        assert len(self.find_organizations(self.__class__.group_name)) == 1
        print('Only one organization with name "{}" exists (as expected)'.format(self.__class__.group_name))

        assert self.get_groups_in_organization_first_page(self.__class__.org_id)["totalCount"] == groups_in_org
        group = self.query_group(self.__class__.group_name)
        assert group is not None
        assert group["orgId"] == self.__class__.org_id

        version = KubernetesTester.om_version()
        expected_tags = [self.namespace[:MAX_TAG_LEN].upper()]

        if should_include_tag(version):
            expected_tags.append(EXTERNALLY_MANAGED_TAG)

        assert sorted(group["tags"]) == sorted(expected_tags)

        print('Only one group with name "{}" exists (as expected)'.format(self.__class__.group_name))

    @staticmethod
    def create_organizations(count):
        ids = []
        for i in range(0, count):
            ids.append(KubernetesTester.create_organization(KubernetesTester.random_k8s_name("fake-{}-".format(i))))
            if (i + 1) % 100 == 0:
                print("Created {} fake organizations".format(i + 1))
        return ids

    @staticmethod
    def create_groups(org_id, count):
        ids = []
        for i in range(0, count):
            ids.append(
                KubernetesTester.create_group(org_id, KubernetesTester.random_k8s_name("fake-group-{}-".format(i)))
            )
            if (i + 1) % 100 == 0:
                print("Created {} fake groups inside organization {}".format(i + 1, org_id))
        return ids

    @classmethod
    def teardown_env(cls):
        # Remove fake organizations from Ops Manager, except for the organization that was used by the standalone
        for i, id in enumerate(cls.all_orgs_ids):
            cls.remove_organization(id)
            if (i + 1) % 100 == 0:
                print("Removed {} fake organizations".format(i))

        # Remove fake groups from Ops Manager
        for i, id in enumerate(cls.all_groups_ids):
            cls.remove_group(id)
            if (i + 1) % 100 == 0:
                print("Removed {} fake groups inside organizationn {}".format(i, cls.org_id))
