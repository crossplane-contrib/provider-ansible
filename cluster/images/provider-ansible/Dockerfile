FROM python:3.12.1-alpine3.19 AS build-base
RUN apk --no-cache add gcc musl-dev libffi-dev
RUN mkdir -p /wheels
RUN python -m pip wheel ansible ansible-runner --wheel-dir=/wheels

FROM python:3.12.1-alpine3.19
RUN apk --no-cache add ca-certificates bash openssh-client git
COPY --from=build-base /wheels/* /wheels/
RUN python -m pip install --no-index --find-links=/wheels ansible ansible-runner && \
    rm -r /wheels

ARG TARGETOS
ARG TARGETARCH

ADD bin/$TARGETOS\_$TARGETARCH/provider /usr/local/bin/crossplane-ansible-provider

# As of Crossplane v1.3.0 provider controllers run as UID 2000.
# https://github.com/crossplane/crossplane/blob/v1.3.0/internal/controller/pkg/revision/deployment.go#L32
RUN adduser --disabled-password --uid 2000 ansible
RUN passwd -d ansible

RUN mkdir /ansibleDir /.ansible
RUN chown ansible /ansibleDir /.ansible


EXPOSE 8080
USER ansible
ENTRYPOINT ["crossplane-ansible-provider"]