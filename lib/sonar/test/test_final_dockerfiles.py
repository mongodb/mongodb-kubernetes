from unittest import skip

from ..sonar import process_image


@skip("This test case is only used to generate the final Dockerfile for ops-manager")
def test_build_om_dockerfile():
    process_image(
        image_name="ops-manager",
        skip_tags=["release"],
        include_tags=["final_dockerfile"],
        build_args={
            "registry": "localhost:5000",
            "version": "8.0.7",
            "om_download_url": "https://downloads.mongodb.com/on-prem-mms/tar/mongodb-mms-8.0.7.500.20250505T1426Z.tar.gz",
        },
        build_options={},
        inventory="inventories/om.yaml",
    )


@skip("This test case is only used to generate the final Dockerfile for database")
def test_build_database_dockerfile():
    process_image(
        image_name="database",
        skip_tags=["release"],
        include_tags=["final_dockerfile"],
        build_args={
            "registry": "localhost:5000",
            "version": "1.1.0",
        },
        build_options={},
        inventory="inventories/database.yaml",
    )


@skip("This test case is only used to generate the final Dockerfile for init appdb")
def test_build_init_appdb_dockerfile():
    process_image(
        image_name="init-appdb",
        skip_tags=["release"],
        include_tags=["final_dockerfile"],
        build_args={
            "registry": "localhost:5000",
            "version": "1.1.0",
            "is_appdb": True,
            "mongodb_tools_url_ubi": "https://downloads.mongodb.org/tools/db/mongodb-database-tools-rhel93-x86_64-100.12.0.tgz",
        },
        build_options={},
        inventory="inventories/init_appdb.yaml",
    )


@skip("This test case is only used to generate the final Dockerfile for init database")
def test_build_init_database_dockerfile():
    process_image(
        image_name="init-database",
        skip_tags=["release"],
        include_tags=["final_dockerfile"],
        build_args={
            "registry": "localhost:5000",
            "version": "1.1.0",
            "is_appdb": False,
            "mongodb_tools_url_ubi": "https://downloads.mongodb.org/tools/db/mongodb-database-tools-rhel93-x86_64-100.12.0.tgz",
        },
        build_options={},
        inventory="inventories/init_database.yaml",
    )


@skip("This test case is only used to generate the final Dockerfile for init ops manager")
def test_build_init_ops_manager_dockerfile():
    process_image(
        image_name="init-ops-manager",
        skip_tags=["release"],
        include_tags=["final_dockerfile"],
        build_args={
            "registry": "localhost:5000",
            "version": "1.1.0",
        },
        build_options={},
        inventory="inventories/init_om.yaml",
    )


def test_build_operator_dockerfile():
    process_image(
        image_name="mongodb-kubernetes",
        skip_tags=["release"],
        include_tags=["final_dockerfile"],
        build_args={
            "version": "1.1.0",
            "registry": "localhost:5000",
            "release_version": "1.1.0",
            "log_automation_config_diff": "false",
            "use_race": "false",
            "debug": False,
        },
        build_options={},
        inventory="inventory.yaml",
    )
