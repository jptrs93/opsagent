FROM python:3.13-alpine

RUN apk add --no-cache bash ca-certificates curl git tar gzip

ARG OPD_RELEASES="v0.0.160 v0.0.222"
ARG OPD_LATEST_RELEASE="v0.0.222"
ARG OPD_ARCHES="amd64 arm64"
ARG CONTAINERD_VERSION="2.0.5"
ARG RUNC_VERSION="1.2.6"

ENV OPD_RELEASES=${OPD_RELEASES} \
    OPD_LATEST_RELEASE=${OPD_LATEST_RELEASE} \
    OPD_ARCHES=${OPD_ARCHES} \
    CONTAINERD_VERSION=${CONTAINERD_VERSION} \
    RUNC_VERSION=${RUNC_VERSION}

COPY prepare.sh /usr/local/bin/prepare-local-repo
COPY server.py /usr/local/bin/local-repo-server

RUN chmod +x /usr/local/bin/prepare-local-repo /usr/local/bin/local-repo-server \
    && /usr/local/bin/prepare-local-repo

EXPOSE 8080
CMD ["/usr/local/bin/local-repo-server"]
