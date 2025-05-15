# Pipeline

## Environment Variables (env vars)

This listing contains all environment variables used in `pipeline.py`.
Default evergreen-ci expansions can be looked up [here](https://docs.devprod.prod.corp.mongodb.com/evergreen/Project-Configuration/Project-Configuration-Files#expansions).

| Environment Variable           | Usage / Description                                                                |
|-------------------------------|------------------------------------------------------------------------------------|
| `otel_trace_id`                | OpenTelemetry tracing: trace ID. Default evergreen-ci expansion.                   |
| `otel_parent_id`               | OpenTelemetry tracing: parent span ID. Default evergreen-ci expansion.             |
| `otel_collector_endpoint`      | OpenTelemetry tracing: collector endpoint. Default evergreen-ci expansion.         |
| `distro`                       | Image type (defaults to `ubi`)                                                     |
| `BASE_REPO_URL`                | Base repository URL for images                                                     |
| `namespace`                    | Kubernetes namespace (defaults to `default`)                                       |
| `skip_tags`                    | Tags to skip during build                                                          |
| `include_tags`                 | Tags to include during build                                                       |
| `all_agents`                   | Whether to build all agent images                                                  |
| `RUNNING_IN_EVG`               | Whether running in Evergreen pipeline                                              |
| `is_patch`                     | Whether running as a patch build. Default evergreen-ci expansion.                  |
| `pin_tag_at`                   | Time to pin image tag (format: `HH:MM`)                                            |
| `created_at`                   | Build creation time (format: `%y_%m_%d_%H_%M_%S`). Default evergreen-ci expansion. |
| `triggered_by_git_tag`         | Git tag that triggered the build. Default evergreen-ci expansion. Default evergreen-ci expansion.                 |
| `version_id`                   | Patch ID or version for non-release builds. Default evergreen-ci expansion.        |
| `test_suffix`                  | Suffix for test images                                                             |
| `LOG_AUTOMATION_CONFIG_DIFF`   | Whether to log automation config diff                                              |
| `PYTHON_VERSION`               | Python version for test images                                                     |
| `GOLANG_VERSION`               | Go version for community images and tests                                          |
| `QUAY_REGISTRY`                | Quay registry URL (defaults to `quay.io/mongodb`)                                  |
| `REGISTRY`                     | ECR registry URL (defaults to `268558157000.dkr.ecr.us-east-1.amazonaws.com/dev`)  |
| `om_version`                   | Ops Manager version for OM image builds                                            |
| `om_download_url`              | Download URL for Ops Manager (optional, can be auto-detected)                      |

## Context Image Build Process

```
                      ┌─────────────────────────────┐
                      │       Release Pipeline       │
                      └────────────┬────────────────┘
                                   │
                                   ▼
                     ┌─────────────────────────────────┐
                     │  Build context image            │
                     │  Tag: opsmanager-context:1.33.0 │
                     └────────────┬────────────────────┘
                                   │
                                   ▼
                     ┌───────────────────────────────┐
                     │         Daily Build           │
                     │   Base: opsmanager-context    │
                     │   Input tag: 1.33.0           │
                     └────────────┬──────────────────┘
                                  │
                                  ▼
                    ┌────────────────────────────────────┐
                    │       Push Two Image Tags          │
                    └────────────┬───────────────┬───────┘
                                 ▼               ▼
             ┌────────────────────────┐ ┌──────────────────────────────┐
             │ Rolling Tag (latest)   │ │ Immutable Tag (daily stamp)  │
             │ opsmanager:1.33.0      │ │ opsmanager:1.33.0-2025-01-01 │
             └────────────────────────┘ └──────────────────────────────┘

                                 ▼ (next day build)
             ┌────────────────────────┐ ┌──────────────────────────────┐
             │ opsmanager:1.33.0      │ │ opsmanager:1.33.0-2025-01-02 │
             └────────────────────────┘ └──────────────────────────────┘
               ↑ now updated to point       ↑ new image pushed
                 to the 2025-01-02 build
```
