SSDLC Compliance Report: MongoDB Enterprise Operator ${VERSION}
=================================================================

- Release Creators: ${AUTHOR}
- Created On:       ${DATE}

Overview:

- **Product and Release Name**

  - MongoDB Enterprise Operator ${VERSION}, ${DATE}.
  - Release Type: ${RELEASE_TYPE}

- **Process Document**
  - http://go/how-we-develop-software-doc

- **Tool used to track third party vulnerabilities**
  - Snyk

- **Dependency Information**
  - See SBOMS Lite manifests (CycloneDX in JSON format for the SBOM and JSON for the supplementary report on CVEs):
    ${SBOMS}

- **Static Analysis Report**
  - We use GoSec for static analysis scanning on our CI tests. There are no findings (neither critical nor high) unresolved.

- **Release Signature Report**
  - Image signatures enforced by CI pipeline.
  - Signatures verification: documentation in-progress: https://jira.mongodb.org/browse/DOCSP-39646

- **Security Testing Report**
  - Sast: https://jira.mongodb.org/browse/CLOUDP-251553
  - Pentest: (Same as the others) https://jira.mongodb.org/browse/CLOUDP-251555
  - Dast: We decided not to do per https://jira.mongodb.org/browse/CLOUDP-251554 and the linked scope

- **Security Assessment Report**
  - https://jira.mongodb.org/browse/CLOUDP-251555

Assumptions and attestations:

1. Internal processes are used to ensure CVEs are identified and mitigated within SLAs.

2. The Dependency document does not specify third party OSS CVEs fixed by the release and the date we discovered them.

3. There is no CycloneDX field for original/modified CVSS score or discovery date. The `x-` prefix indicates this.
