# This image is based on latest Python 3.6 release in latest Debian Stretch.
# I had to move away from Alpine as the latest Kubernetes Python module depends
# on `cryptography` which can be installed in Debian but needs to be compiled
# in Alpine, meaning that we would have to install gcc or clang on it, making
# it too slow for the images.
#
# Ref: https://cryptography.io/en/latest/installation/#building-cryptography-on-linux
#
FROM python:3.6-slim

WORKDIR /tests
RUN mkdir -p /tests/fixtures
COPY requirements.txt /tests/

RUN python3 -m pip install -r requirements.txt

COPY standalone_tests /tests
COPY standalone_tests/fixtures /tests/fixtures

COPY replicaset_tests /tests
COPY replicaset_tests/fixtures /tests/fixtures

COPY shardedcluster_tests /tests
COPY shardedcluster_tests/fixtures /tests/fixtures

COPY tls_tests /tests
COPY tls_tests/fixtures /tests/fixtures

COPY mixed /tests


COPY kubetester.py /tests/
