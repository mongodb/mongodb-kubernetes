import pytest
from kubetester.kubetester import (
    EXTERNALLY_MANAGED_TAG,
    MAX_TAG_LEN,
    KubernetesTester,
    fixture,
)
from kubetester.omtester import should_include_tag, skip_if_cloud_manager


@pytest.mark.e2e_standalone_groups
class TestStandaloneOrganizationSpecified(KubernetesTester):
    """
    name: Test for config map with specified organization id.
    description: |
      Tests the configuration in config map with organization specified which already exists. The group
      must be created automatically.
      Skipped in Cloud Manager as programmatic API keys won't allow for organizations to be created.
    """

    org_id = None
    org_name = None

    @classmethod
    def setup_env(cls):
        # Create some organization and update the config map with its organization id
        cls.org_name = KubernetesTester.random_k8s_name("standalone-group-test-")
        cls.org_id = cls.create_organization(cls.org_name)
        cls.patch_config_map(cls.get_namespace(), "my-project", {"orgId": cls.org_id})

        print("Patched config map, now it references organization " + cls.org_id)

    # todo
    @pytest.mark.skip(
        reason="project reconciliation adds some flakiness - sometimes it gets on time to create the "
        "project in OM before this method is called - should be fixed by one project"
    )
    def test_standalone_created_organization_found(self):
        groups_in_org = self.get_groups_in_organization_first_page(self.__class__.org_id)["totalCount"]

        # no group is created when organization is created
        assert groups_in_org == 0

    def test_standalone_cr_is_created(self):
        # Create a standalone - the organization will be found and new group will be created
        self.create_custom_resource_from_file(self.get_namespace(), fixture("standalone.yaml"))

        KubernetesTester.wait_until("in_running_state", 150)

    def test_standalone_organizations_are_found(self):
        # Making sure no more organizations were created but the group was created inside the organization
        assert len(self.find_organizations(self.__class__.org_name)) == 1
        print('Only one organization with name "{}" exists (as expected)'.format(self.__class__.org_name))

    def test_standalone_get_groups_in_orgs(self):
        page = self.get_groups_in_organization_first_page(self.__class__.org_id)
        assert page["totalCount"] == 1
        group = page["results"][0]
        assert group is not None
        assert group["orgId"] == self.__class__.org_id

        print(
            'The group "{}" has been created by the Operator in organization "{}"'.format(
                self.get_om_group_name(), self.__class__.org_name
            ),
        )

    @skip_if_cloud_manager()
    def test_group_tag_was_set(self):
        page = self.get_groups_in_organization_first_page(self.__class__.org_id)
        group = page["results"][0]

        version = KubernetesTester.om_version()
        expected_tags = [self.namespace[:MAX_TAG_LEN].upper()]

        if should_include_tag(version):
            expected_tags.append(EXTERNALLY_MANAGED_TAG)

        assert sorted(group["tags"]) == sorted(expected_tags)
