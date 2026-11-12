# Agent scripts for the local proxy e2e

These three files are the `docker/mongodb-agent` scripts as they were before commit `1e25c541d` ("Keep the legacy agent
scripts and resolve the launcher library"). They find the init-database scripts and probes under `MDB_INIT_ROOT`
(an image volume) when it is set, and behave as the legacy scripts when it is not.

`docker/mongodb-agent` has the legacy versions again: the legacy static architecture reaches the init-database
container through the shared process namespace, and those files must not change. A pod in proxy mode has no such
container, so the development agent image that `scripts/dev/proxy_e2e.sh` builds uses these copies instead.

Delete this directory when the agent image of the stack (PRs 1884 and 1876) is the one the e2e runs with.
