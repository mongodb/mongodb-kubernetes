FROM ubuntu:22.04

ARG TARGETARCH

RUN apt-get update && apt-get install -y \
    ca-certificates \
    curl \
    socat \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /var/log/monarch

# Monarch binary — EA code-freeze release `0.1.0-a155746` (validated against the
# playbook + e2e on 28 May 2026; resolves CLOUDP-404411 by using the `__system`
# keyfile user for outbound mongod auth, so the operator does not need to embed
# `mms-shipper` credentials in the injector's mongodURIs).
#
# The SHA256 of each tarball is pinned to prevent silent content drift at the
# upstream URL. Internal-downloads has in the past republished the same URL
# with a rebuilt binary (e.g. after the monarchApiPort → monarchApiEndpoint
# rename). Without a checksum check, Docker's layer cache will happily reuse
# the old image even when the upstream content has changed, causing hard-to-
# diagnose binding/flag mismatches downstream.
#
# URL scheme changed in CLOUDP-404176: <commit>/amazon2-x86/ → <semver>/rhel9-x86/.
# To adopt a newer binary: update MONARCH_VERSION AND both SHA256 values below.
# A mismatched checksum fails the build with a clear diff, forcing an audit.
ARG MONARCH_VERSION=0.1.0-a155746
ARG MONARCH_AMD64_SHA256=8fd171200e08184eec723299aae79f4bd60d2cda3adfe874ddbff41131bec080
ARG MONARCH_ARM64_SHA256=65a08369a6dc6acec19af70fda09d30717f6b8ddd05eddaded54b48635ff507c
RUN case "${TARGETARCH}" in \
        amd64) MONARCH_URL="https://internal-downloads.mongodb.com/monarch-dev/${MONARCH_VERSION}/rhel9-x86/monarch.tgz"; \
               EXPECTED_SHA="${MONARCH_AMD64_SHA256}" ;; \
        arm64) MONARCH_URL="https://internal-downloads.mongodb.com/monarch-dev/${MONARCH_VERSION}/rhel9-arm64/monarch.tgz"; \
               EXPECTED_SHA="${MONARCH_ARM64_SHA256}" ;; \
        *) echo "Unsupported architecture: ${TARGETARCH}" && exit 1 ;; \
    esac && \
    curl -fsSL "${MONARCH_URL}" -o /tmp/monarch.tgz && \
    echo "${EXPECTED_SHA}  /tmp/monarch.tgz" | sha256sum -c - && \
    tar -xz -C /usr/local/bin/ -f /tmp/monarch.tgz && \
    rm /tmp/monarch.tgz && \
    chmod +x /usr/local/bin/monarch

COPY entrypoint.sh /usr/local/bin/monarch-entrypoint.sh
RUN chmod +x /usr/local/bin/monarch-entrypoint.sh

EXPOSE 8080
EXPOSE 9995

ENTRYPOINT ["/usr/local/bin/monarch-entrypoint.sh"]
