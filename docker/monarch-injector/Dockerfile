FROM ubuntu:22.04

ARG TARGETARCH

RUN apt-get update && apt-get install -y \
    ca-certificates \
    curl \
    socat \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /var/log/monarch

# These URLs are the same binaries used by automation and OM for testing.
# To update the monarch version, replace both URLs with the new commit's paths.
RUN case "${TARGETARCH}" in \
        amd64) MONARCH_URL="https://internal-downloads.mongodb.com/monarch-dev/monarch_22ceab12b0f04eb91121cabd7e008fea5d7a2a72/amazon2-x86/monarch.tgz" ;; \
        arm64) MONARCH_URL="https://internal-downloads.mongodb.com/monarch-dev/monarch_22ceab12b0f04eb91121cabd7e008fea5d7a2a72/amazon2023-arm64/monarch.tgz" ;; \
        *) echo "Unsupported architecture: ${TARGETARCH}" && exit 1 ;; \
    esac && \
    curl -fsSL "${MONARCH_URL}" | \
    tar -xz -C /usr/local/bin/ && \
    chmod +x /usr/local/bin/monarch

COPY entrypoint.sh /usr/local/bin/monarch-entrypoint.sh
RUN chmod +x /usr/local/bin/monarch-entrypoint.sh

EXPOSE 8080
EXPOSE 9995

ENTRYPOINT ["/usr/local/bin/monarch-entrypoint.sh"]
