FROM python:3.10-alpine3.17
RUN apk --no-cache add ca-certificates bash openssh-client git
RUN python -m pip install ansible ansible-runner

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