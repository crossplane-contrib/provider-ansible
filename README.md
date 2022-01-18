<!-- START doctoc generated TOC please keep comment here to allow auto update -->
<!-- DON'T EDIT THIS SECTION, INSTEAD RE-RUN doctoc TO UPDATE -->
**Table of Contents**  *generated with [DocToc](https://github.com/thlorenz/doctoc)*

- [Crossplane Provider for Ansible](#crossplane-provider-for-ansible)
  - [Overview](#overview)
  - [Getting Started and Documentation](#getting-started-and-documentation)
  - [Report a Bug](#report-a-bug)
  - [Developer guide](#developer-guide)
    - [Run against a Kubernetes cluster](#run-against-a-kubernetes-cluster)
    - [Basic Usage](#basic-usage)
  - [Additional documents](#additional-documents)
  - [Licensing](#licensing)

<!-- END doctoc generated TOC please keep comment here to allow auto update -->

# Crossplane Provider for Ansible

## Overview

provider-ansible is the Crossplane infrastructure provider for Ansible.

The Ansible provider adds support for a `PlaybookSet` managed resource that
represents an Ansible Playbook(s). The configuration of each playbook may be
either fetched from a remote source (e.g. git), or simply specified inline.


## Getting Started and Documentation

For getting started guides, installation, deployment, and administration, check latest
Crossplane [document](https://crossplane.io/docs/latest).

## Report a Bug

For filing bugs, suggesting improvements, or requesting new features, please
open an [issue](https://github.com/cloud-pak-gitops/crossplane-provider-ansible/issues).

## Developer guide

`Ansible-provider` use [kind](https://github.com/kubernetes-sigs/kind) to run local Kubernetes clusters using Docker container "nodes".

### Run against a Kubernetes cluster

If you have [go (1.16+)](https://golang.org/doc/devel/release.html#policy) and [docker](https://www.docker.com/) installed 

```console
make dev
```
is all you need!

clean the dev environement:
```console
make dev-clean
```

Build, push, and install:

```console
make all
```

Build image:

```console
make image
```

Push image:

```console
make push
```

Compiling dna from source:

```console
make build
```

### Basic Usage

To list crds:
```console
kubectl get crds
```

## Additional documents

- [`GO`](https://tecadmin.net/install-go-on-debian/): install go1.17+ on debian
- [`DOCKER`](https://docs.docker.com/engine/install/debian/): install docker on debian

## Licensing

provider-ansible is under the Apache 2.0 license.

[![FOSSA
Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fcrossplane%2Fprovider-gcp.svg?type=large)](https://app.fossa.io/projects/git%2Bgithub.com%2Fcrossplane%2Fprovider-gcp?ref=badge_large)