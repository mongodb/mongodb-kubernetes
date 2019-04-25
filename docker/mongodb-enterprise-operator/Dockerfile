FROM ubuntu:16.04

# Add certificate in order to make the operator work with Cloud Manager
RUN apt-get -qq update && \
  apt-get -y -qq install ca-certificates curl && \
  apt-get upgrade -y -qq && \
  apt-get dist-upgrade -y -qq && \
  rm -rf /var/lib/apt/lists/*

# Installs the cfssl packages to generate Certificate Signing Request (CSR) files
# Will also verify that the downloaded files are correct by using the SHA256 provided
# in the cfssl website.
RUN \
           curl -sO https://pkg.cfssl.org/R1.2/cfssl_linux-amd64 \
        && curl -sO https://pkg.cfssl.org/R1.2/cfssljson_linux-amd64 \
        && curl -sO https://pkg.cfssl.org/R1.2/SHA256SUMS \
        && cat SHA256SUMS | grep cfssl_linux-amd64 | sha256sum -c - \
        && cat SHA256SUMS | grep cfssljson_linux-amd64 | sha256sum -c - \
        && mv cfssljson_linux-amd64 /usr/local/bin/cfssljson \
        && chmod +x /usr/local/bin/cfssljson \
        && mv cfssl_linux-amd64 /usr/local/bin/cfssl \
        && chmod +x /usr/local/bin/cfssl

ADD content/mongodb-enterprise-operator /usr/local/bin/

ENTRYPOINT exec /usr/local/bin/mongodb-enterprise-operator
