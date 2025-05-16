# the init database image gets supplied by pipeline.py and corresponds to the operator version we want to release
# the agent with. This enables us to release the agent for older operator.
ARG init_database_image
FROM ${init_database_image} as init_database

FROM public.ecr.aws/docker/library/golang:1.24 as dependency_downloader

WORKDIR /go/src/github.com/mongodb/mongodb-kubernetes/

COPY go.mod go.sum ./

RUN go mod download

FROM public.ecr.aws/docker/library/golang:1.24 as readiness_builder

WORKDIR /go/src/github.com/mongodb/mongodb-kubernetes/

COPY --from=dependency_downloader /go/pkg /go/pkg
COPY . /go/src/github.com/mongodb/mongodb-kubernetes

RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -o /readinessprobe ./mongodb-community-operator/cmd/readiness/main.go
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -o /version-upgrade-hook ./mongodb-community-operator/cmd/versionhook/main.go

FROM scratch
ARG mongodb_tools_url_ubi
ARG mongodb_agent_url_ubi

COPY --from=readiness_builder /readinessprobe /data/
COPY --from=readiness_builder /version-upgrade-hook /data/

ADD ${mongodb_tools_url_ubi} /data/mongodb_tools_ubi.tgz
ADD ${mongodb_agent_url_ubi} /data/mongodb_agent_ubi.tgz

COPY --from=init_database /probes/probe.sh /data/probe.sh
COPY --from=init_database /scripts/agent-launcher-lib.sh /data/
COPY --from=init_database /scripts/agent-launcher.sh /data/
COPY --from=init_database /licenses/LICENSE /data/
