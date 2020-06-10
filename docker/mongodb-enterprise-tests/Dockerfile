# This image is based on latest Python 3.6 release in latest Debian Stretch.
# I had to move away from Alpine as the latest Kubernetes Python module depends
# on `cryptography` which can be installed in Debian but needs to be compiled
# in Alpine, meaning that we would have to install gcc or clang on it, making
# it too slow for the images.
#
# Ref: https://cryptography.io/en/latest/installation/#building-cryptography-on-linux
#
FROM python:3.7-slim

ENV HELM_NAME "helm-v3.2.0-linux-amd64.tar.gz"

RUN apt-get -qq update \
        && apt-get -y -qq install \
        curl

# install Helm
RUN curl --fail --retry 3 -L -o "${HELM_NAME}" "https://get.helm.sh/${HELM_NAME}" \
    && tar -xzf "${HELM_NAME}" \
    && rm "${HELM_NAME}" \
    && mv "linux-amd64/helm" "/usr/local/bin/helm"

RUN mkdir /tests
WORKDIR /tests
COPY requirements.txt /tests/

RUN python3 -m pip install -r requirements.txt

# copying the test files after python build, otherwise pip install will be called each time the tests change
COPY . /tests
COPY helm_chart /helm_chart

