#!/usr/bin/env python3
import sys

from shrub.v2 import BuildVariant, FunctionCall, ShrubProject, Task, TaskGroup


def define_task(deployments, replicas, v):
    name = f"perf_{deployments}d_{replicas}r_{v}v"

    return Task(
        name,
        [
            FunctionCall(
                "e2e_test_perf",
                {
                    "PERF_TASK_REPLICAS": replicas,
                    "PERF_TASK_DEPLOYMENTS": deployments,
                    "TEST_NAME_OVERRIDE": "e2e_om_reconcile_perf",
                },
            ),
        ],
    )


variant = sys.argv[1]
size = sys.argv[2]

if size == "small":
    tasks = {define_task(d, r, sys.argv[1]) for d, r in ((300, 1), (60, 3), (180, 1), (90, 2))}
else:
    tasks = {define_task(d, r, sys.argv[1]) for d, r in ((300, 1), (400, 1), (130, 3), (500, 1))}

group = TaskGroup(
    f"e2e_operator_perf_task_group_{variant}",
    tasks=list(tasks),
    max_hosts=30,
    setup_group=[
        FunctionCall("clone"),
        FunctionCall("download_kube_tools"),
        FunctionCall("setup_building_host"),
    ],
    setup_task=[
        FunctionCall("cleanup_exec_environment"),
        FunctionCall("configure_docker_auth"),
        FunctionCall("setup_kubernetes_environment"),
        FunctionCall("setup_cloud_qa"),
    ],
    teardown_task=[
        FunctionCall("upload_e2e_logs"),
        FunctionCall("teardown_kubernetes_environment"),
        FunctionCall("teardown_cloud_qa"),
    ],
    teardown_group=[FunctionCall("prune_docker_resources"), FunctionCall("run_retry_script")],
)

build_variant = BuildVariant(variant).display_task(variant)
build_variant.add_task_group(group)
project = ShrubProject({build_variant})

print(project.json())
