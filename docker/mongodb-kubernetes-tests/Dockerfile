# This image is based on latest Python 3.6 release in latest Debian Stretch.
# I had to move away from Alpine as the latest Kubernetes Python module depends
# on `cryptography` which can be installed in Debian but needs to be compiled
# in Alpine, meaning that we would have to install gcc or clang on it, making
# it too slow for the images.
#
# Ref: https://cryptography.io/en/latest/installation/#building-cryptography-on-linux
#
ARG PYTHON_VERSION

FROM public.ecr.aws/docker/library/python:${PYTHON_VERSION}-slim as builder

RUN apt-get -qq update \
    && apt-get -y -qq install \
    curl libldap2-dev libsasl2-dev build-essential git libssl-dev pkg-config

COPY requirements.txt requirements.txt

RUN python3 -m venv /venv && . /venv/bin/activate && pip install --upgrade pip && GRPC_PYTHON_BUILD_SYSTEM_OPENSSL=1 pip install -r requirements.txt

FROM scratch AS tools_downloader

ARG mongodb_tools_url="https://fastdl.mongodb.org/tools/db"

ARG mongodb_tools_version_amd64="mongodb-database-tools-rhel93-x86_64-100.12.0.tgz"
ADD "${mongodb_tools_url}/${mongodb_tools_version_amd64}" /data/amd64/mongodb_tools.tgz

ARG mongodb_tools_version_arm64="mongodb-database-tools-rhel93-aarch64-100.12.0.tgz"
ADD "${mongodb_tools_url}/${mongodb_tools_version_arm64}" /data/arm64/mongodb_tools.tgz

ARG mongodb_tools_version_s390x="mongodb-database-tools-rhel9-s390x-100.12.0.tgz"
ADD "${mongodb_tools_url}/${mongodb_tools_version_s390x}" /data/s390x/mongodb_tools.tgz

ARG mongodb_tools_version_ppc64le="mongodb-database-tools-rhel9-ppc64le-100.12.0.tgz"
ADD "${mongodb_tools_url}/${mongodb_tools_version_ppc64le}" /data/ppc64le/mongodb_tools.tgz

FROM public.ecr.aws/docker/library/python:${PYTHON_VERSION}-slim

ARG TARGETARCH
COPY --from=tools_downloader "/data/${TARGETARCH}/mongodb_tools.tgz" /tmp/mongodb-tools.tgz

RUN apt-get -qq update \
    && apt-get -y -qq install \
    curl \
    libldap2-dev \
    libsasl2-dev \
    git \
    openssl \
    unzip

RUN mkdir -p /tmp/mongodb-tools && \
    tar xfz /tmp/mongodb-tools.tgz -C /tmp/mongodb-tools && \
    cp /tmp/mongodb-tools/*/bin/* /usr/local/bin/ && \
    rm -rf /tmp/mongodb-tools /tmp/mongodb-tools.tgz


ENV HELM_NAME "helm-v3.17.1-linux-${TARGETARCH}.tar.gz"

# install Helm
RUN curl --fail --retry 3 -L -o "${HELM_NAME}" "https://get.helm.sh/${HELM_NAME}" \
    && tar -xzf "${HELM_NAME}" \
    && rm "${HELM_NAME}" \
    && mv "linux-${TARGETARCH}/helm" "/usr/local/bin/helm"

# install kubectl
RUN curl -LO "https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/linux/${TARGETARCH}/kubectl" \
    && chmod +x ./kubectl \
    && mv ./kubectl /usr/local/bin/kubectl

# install aws, required to run helm registry login while running the tests
RUN curl "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o "awscliv2.zip" \
    && unzip -q awscliv2.zip \
    && ./aws/install

COPY --from=builder /venv /venv

ENV PATH="/venv/bin:${PATH}"

RUN mkdir /tests
WORKDIR /tests

# copying the test files after python build, otherwise pip install will be called each time the tests change
COPY . /tests
# copying the helm_chart directory as well to support installation of the Operator from the test application
COPY helm_chart /helm_chart
COPY release.json /release.json
# we use the public directory to automatically test resources samples
COPY public /mongodb-kubernetes/public

ADD "multi-cluster-kube-config-creator_${TARGETARCH}" /usr/local/bin/multi-cluster-kube-config-creator
