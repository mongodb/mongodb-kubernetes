# This image is based on latest Python 3.6 release in latest Debian Stretch.
# I had to move away from Alpine as the latest Kubernetes Python module depends
# on `cryptography` which can be installed in Debian but needs to be compiled
# in Alpine, meaning that we would have to install gcc or clang on it, making
# it too slow for the images.
#
# Ref: https://cryptography.io/en/latest/installation/#building-cryptography-on-linux
#
ARG PYTHON_VERSION

FROM --platform=linux/amd64 public.ecr.aws/docker/library/python:${PYTHON_VERSION}-slim as builder


RUN apt-get -qq update \
    && apt-get -y -qq install \
    curl libldap2-dev libsasl2-dev build-essential git

COPY requirements.txt requirements.txt

RUN python3 -m venv /venv && . /venv/bin/activate && python3 -m pip install -r requirements.txt


FROM --platform=linux/amd64 public.ecr.aws/docker/library/python:${PYTHON_VERSION}-slim

RUN apt-get -qq update \
    && apt-get -y -qq install \
    curl \
    libldap2-dev \
    libsasl2-dev \
    git \
    openssl

ENV HELM_NAME "helm-v3.17.1-linux-amd64.tar.gz"
# install Helm
RUN curl --fail --retry 3 -L -o "${HELM_NAME}" "https://get.helm.sh/${HELM_NAME}" \
    && tar -xzf "${HELM_NAME}" \
    && rm "${HELM_NAME}" \
    && mv "linux-amd64/helm" "/usr/local/bin/helm"

<<<<<<< HEAD:docker/mongodb-kubernetes-tests/Dockerfile
ADD https://fastdl.mongodb.org/tools/db/mongodb-database-tools-ubuntu2204-x86_64-100.12.0.tgz /tmp/mongodb-tools.tgz
RUN mkdir -p /tmp/mongodb-tools && \
    tar xfz /tmp/mongodb-tools.tgz -C /tmp/mongodb-tools && \
    cp /tmp/mongodb-tools/*/bin/* /usr/local/bin/ && \
    rm -rf /tmp/mongodb-tools /tmp/mongodb-tools.tgz

RUN curl -LO "https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/linux/amd64/kubectl" \
    && chmod +x ./kubectl \
    && mv ./kubectl /usr/local/bin/kubectl

<<<<<<< HEAD:docker/mongodb-kubernetes-tests/Dockerfile
>>>>>>> bb976bad4 (Add kubectl download to test_app dockerfile):docker/mongodb-enterprise-tests/Dockerfile
=======
>>>>>>> 25ba545ed (Fixed dockerfile):docker/mongodb-enterprise-tests/Dockerfile
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

ADD multi-cluster-kube-config-creator_linux /usr/local/bin/multi-cluster-kube-config-creator
