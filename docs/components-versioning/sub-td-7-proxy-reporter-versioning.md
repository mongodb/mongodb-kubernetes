# Sub-TD 7: Versioning and release of mongodb-proxy and mongodb-reporter

Owner: Maciej Karaś

`mongodb-proxy` and `mongodb-reporter` are built from the MCK repository and released with every MCK release. Their version is always the MCK version, the same way the init images (`init-database` and `init-ops-manager`) work today. This sub-TD keeps it that way for now. Whether to improve it is investigated separately in the [Engineering Proposal: Versioning and release of MCK components](https://docs.google.com/document/d/1KNy_g48DwgBpPrliFeqb9hel7tbQpxZ9C-XCslrg2X8/edit?tab=t.0).

| Image | Used by | Helm values | Operator env var |
|---|---|---|---|
| mongodb-proxy | Every static database Pod, mounted into the mongodb container via OCI Images | proxy.name, proxy.version | MDB_PROXY_IMAGE |
| mongodb-reporter | Headless only (AppDB and MongoDBCommunity), sidecar | reporter.name, reporter.version | MDB_REPORTER_IMAGE |

The names follow the existing agent.name and agent.version values. The env vars follow MDB_SEARCH_IMAGE. Both images get RELATED_IMAGE_* entries for OLM and image mirroring.

## Versioning

- **Version:** the image tags are MCK versions (for example `mongodb-proxy:1.16.0`). There is no separate semver.
- **Release:** every MCK release builds, signs and publishes both images at the MCK version, as the init images already do. The release pipeline does not change.
- **`release.json`:** `proxyVersion` and `reporterVersion` always equal `mongodbOperator`. The existing release scripts keep them in step with the operator version, like `initDatabaseVersion` and `initOpsManagerVersion`, and the Helm values, the OLM values and the yaml follow.
- **Backports:** MCK versions are unique across branches, so a fix backported to `release-vN` is an ordinary MCK patch release that publishes both images at that version. There is no version clash.
- **Delivery:** only through MCK releases.

## Rolling restarts

Whilst the rolling restart behaviour will be greatly improved by the fixes in the Cloud Native Agent project, the frequency of rolling restarts that happen with each MCK release is still a problem, just reduced. The proxy and the reporter versions are aligned with the MCK version, so every MCK upgrade still changes their image tags. That is a Pod template change, which causes a rolling restart.

Possible improvements, such as moving a component tag only when the component changed, are investigated in the Engineering Proposal linked above and are not part of this sub-TD.
