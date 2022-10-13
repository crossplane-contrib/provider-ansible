FROM alpinelinux/ansible
RUN apk --no-cache add ca-certificates bash
RUN pip3 install ansible-runner requests

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
