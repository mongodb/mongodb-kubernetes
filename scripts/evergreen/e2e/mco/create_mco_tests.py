from shrub.v2 import BuildVariant, ShrubProject, TaskGroup
from shrub.v2.command import FunctionCall, gotest_parse_files
from shrub.v2.task import Task

# Define the list of dynamically generated task names
task_names = [
    "replica_set",
    "replica_set_enterprise_upgrade_4_5",
    "replica_set_enterprise_upgrade_5_6",
    "replica_set_enterprise_upgrade_6_7",
    "replica_set_enterprise_upgrade_7_8",
    "replica_set_recovery",
    "replica_set_mongod_readiness",
    "replica_set_scale",
    "replica_set_scale_down",
    "replica_set_change_version",
    "feature_compatibility_version",
    "prometheus",
    "replica_set_tls",
    "replica_set_tls_recreate_mdbc",
    "replica_set_tls_rotate",
    "replica_set_tls_rotate_delete_sts",
    "replica_set_tls_upgrade",
    "statefulset_arbitrary_config",
    "statefulset_arbitrary_config_update",
    "replica_set_mongod_config",
    "replica_set_cross_namespace_deploy",
    "replica_set_custom_role",
    "replica_set_arbiter",
    "replica_set_custom_persistent_volume",
    "replica_set_mount_connection_string",
    "replica_set_mongod_port_change_with_arbiters",
    "replica_set_operator_upgrade",
    "replica_set_connection_string_options",
    "replica_set_x509",
    "replica_set_remove_user",
]

tasks = []
# Create dynamically generated tasks
for task_name in task_names:
    task = Task(
        name=task_name,
        commands=[
            FunctionCall(name="e2e_test"),
        ],
    )
    tasks.append(task)

group = TaskGroup(
    f"e2e_mco_task_group",
    tasks=tasks,
    max_hosts=-1,
    setup_group=[
        FunctionCall("clone"),
        FunctionCall("download_kube_tools"),
        FunctionCall("setup_building_host"),
    ],
    setup_task=[
        FunctionCall("cleanup_exec_environment"),
        FunctionCall("configure_docker_auth"),
        FunctionCall("setup_kubernetes_environment"),
    ],
    teardown_task=[
        FunctionCall("upload_e2e_logs_gotest"),
        FunctionCall("teardown_kubernetes_environment"),
    ],
    teardown_group=[FunctionCall("prune_docker_resources"), FunctionCall("run_retry_script")],
)


build_variant = BuildVariant("e2e_mco_tests").display_task("e2e_mco_tests")
build_variant.add_task_group(group)
project = ShrubProject({build_variant})

print(project.yaml())
