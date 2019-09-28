# This image is based on latest Python 3.6 release in latest Debian Stretch.
# I had to move away from Alpine as the latest Kubernetes Python module depends
# on `cryptography` which can be installed in Debian but needs to be compiled
# in Alpine, meaning that we would have to install gcc or clang on it, making
# it too slow for the images.
#
# Ref: https://cryptography.io/en/latest/installation/#building-cryptography-on-linux
#
FROM python:3.7-slim

RUN mkdir /tests
WORKDIR /tests
COPY requirements.txt /tests/

RUN python3 -m pip install -r requirements.txt

COPY . /tests
