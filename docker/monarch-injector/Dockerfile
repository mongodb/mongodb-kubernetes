FROM ubuntu:22.04

ARG MONARCH_COMMIT
ARG TARGETARCH

RUN apt-get update && apt-get install -y \
    ca-certificates \
    curl \
    socat \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /var/log/monarch

RUN case "${TARGETARCH}" in \
        amd64) PLATFORM="amazon2023-x86_64" ;; \
        arm64) PLATFORM="amazon2023-arm64" ;; \
        *) echo "Unsupported architecture: ${TARGETARCH}" && exit 1 ;; \
    esac && \
    curl -fsSL "https://internal-downloads.mongodb.com/monarch-dev/monarch_${MONARCH_COMMIT}/${PLATFORM}/monarch.tgz" | \
    tar -xz -C /usr/local/bin/ && \
    chmod +x /usr/local/bin/monarch

COPY entrypoint.sh /usr/local/bin/monarch-entrypoint.sh
RUN chmod +x /usr/local/bin/monarch-entrypoint.sh

EXPOSE 8080
EXPOSE 9995

ENTRYPOINT ["/usr/local/bin/monarch-entrypoint.sh"]
