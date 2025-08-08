# This image is based on latest Python 3.6 release in latest Debian Stretch.
# I had to move away from Alpine as the latest Kubernetes Python module depends
# on `cryptography` which can be installed in Debian but needs to be compiled
# in Alpine, meaning that we would have to install gcc or clang on it, making
# it too slow for the images.
#
# Ref: https://cryptography.io/en/latest/installation/#building-cryptography-on-linux
#
FROM public.ecr.aws/docker/library/golang:1.24 as builder

ENV GO111MODULE=on
ENV GOPATH ""

WORKDIR /tests

COPY docker/mongodb-community-tests/testdata/tls/ testdata/tls/

RUN curl -LO "https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/linux/amd64/kubectl" \
    && chmod +x ./kubectl \
    && mv ./kubectl /usr/local/bin/kubectl \
    && curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 \
    && chmod +x get_helm.sh && ./get_helm.sh


ADD . .

RUN go mod download && go mod verify
