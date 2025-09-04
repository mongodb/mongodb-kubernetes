FROM debian:bullseye-slim

# Install dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    procps \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY bin_linux/mdbdebug /app/
RUN chmod +x /app/mdbdebug

ENTRYPOINT ["/app/mdbdebug"]
