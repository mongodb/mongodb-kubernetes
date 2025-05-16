ARG imagebase
FROM ${imagebase} as base

FROM registry.access.redhat.com/ubi9/ubi-minimal

COPY --from=base /probes/readinessprobe /probes/readinessprobe
