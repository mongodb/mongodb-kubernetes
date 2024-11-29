# This image is based on latest Python 3.6 release in latest Debian Stretch.
# I had to move away from Alpine as the latest Kubernetes Python module depends
# on `cryptography` which can be installed in Debian but needs to be compiled
# in Alpine, meaning that we would have to install gcc or clang on it, making
# it too slow for the images.
#
# Ref: https://cryptography.io/en/latest/installation/#building-cryptography-on-linux
#
FROM --platform=linux/amd64 public.ecr.aws/docker/library/python:3.9-slim as builder


RUN apt-get -qq update \
    && apt-get -y -qq install \
    curl libldap2-dev libsasl2-dev build-essential git

COPY requirements.txt requirements.txt

RUN python3 -m venv /venv && . /venv/bin/activate && python3 -m pip install -r requirements.txt


FROM --platform=linux/amd64 public.ecr.aws/docker/library/python:3.9-slim

RUN apt-get -qq update \
    && apt-get -y -qq install \
    curl \
    libldap2-dev \
    libsasl2-dev \
    git \
    openssl

ENV HELM_NAME "helm-v3.6.3-linux-amd64.tar.gz"
# install Helm
RUN curl --fail --retry 3 -L -o "${HELM_NAME}" "https://get.helm.sh/${HELM_NAME}" \
    && tar -xzf "${HELM_NAME}" \
    && rm "${HELM_NAME}" \
    && mv "linux-amd64/helm" "/usr/local/bin/helm"

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
COPY public /ops-manager-kubernetes/public

ADD multi-cluster-kube-config-creator_linux /usr/local/bin/multi-cluster-kube-config-creator
