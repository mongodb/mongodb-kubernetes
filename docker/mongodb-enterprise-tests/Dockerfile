FROM python:3.6-alpine

WORKDIR /tests
RUN mkdir -p /tests/fixtures
COPY requirements.txt /tests/

RUN python3 -m pip install -r requirements.txt

COPY standalone_tests /tests
COPY replicaset_tests /tests
COPY shardedcluster_tests /tests
COPY mixed /tests
COPY standalone_tests/fixtures /tests/fixtures
COPY replicaset_tests/fixtures /tests/fixtures
COPY shardedcluster_tests/fixtures /tests/fixtures
COPY kubetester.py  /tests/

