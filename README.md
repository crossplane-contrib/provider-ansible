<!-- START doctoc generated TOC please keep comment here to allow auto update -->
<!-- DON'T EDIT THIS SECTION, INSTEAD RE-RUN doctoc TO UPDATE -->
**Table of Contents**  *generated with [DocToc](https://github.com/thlorenz/doctoc)*

- [Crossplane Provider for Ansible](#crossplane-provider-for-ansible)
  - [Overview](#overview)
  - [Getting Started and Documentation](#getting-started-and-documentation)
  - [Contributing](#contributing)
  - [Report a Bug](#report-a-bug)
  - [Contact](#contact)
  - [Governance and Owners](#governance-and-owners)
  - [Code of Conduct](#code-of-conduct)
  - [Developer guide](#developer-guide)
    - [Run against a Kubernetes cluster](#run-against-a-kubernetes-cluster)
  - [Additional documents](#additional-documents)
  - [Namespaced resources (Crossplane v2)](#namespaced-resources-crossplane-v2)
    - [Trust model](#trust-model)
    - [Choosing a provider config](#choosing-a-provider-config)
    - [Secrets are namespace-local](#secrets-are-namespace-local)
    - [ClusterProviderConfig credentials are cluster-wide](#clusterproviderconfig-credentials-are-cluster-wide)
    - [Deletion runs the playbook](#deletion-runs-the-playbook)
    - [Upgrading](#upgrading)
  - [Licensing](#licensing)

<!-- END doctoc generated TOC please keep comment here to allow auto update -->

# Crossplane Provider for Ansible

## Overview

This `provider-ansible` is the Crossplane infrastructure provider for Ansible.

The Ansible provider adds support for the `AnsibleRun` managed resource that
represents the Ansible content(s). The configuration of the ansible content may be
either fetched from a remote source (e.g. git), or simply specified inline.


## Getting Started and Documentation

For getting started guides, installation, deployment, and administration, check latest
Crossplane [document](https://crossplane.io/docs/latest).

## Contributing

provider-ansible is a community driven project and we welcome contributions. See the
Crossplane
[Contributing](https://github.com/crossplane/crossplane/blob/master/CONTRIBUTING.md)
guidelines to get started.

## Report a Bug

For filing bugs, suggesting improvements, or requesting new features, please
open an [issue](https://github.com/crossplane-contrib/provider-ansible/issues).

## Contact

Please use the following to reach members of the community:

* Slack: Join our [slack channel](https://slack.crossplane.io)
* Forums:
  [crossplane-dev](https://groups.google.com/forum/#!forum/crossplane-dev)
* Twitter: [@crossplane_io](https://twitter.com/crossplane_io)
* Email: [info@crossplane.io](mailto:info@crossplane.io)

## Governance and Owners

`provider-ansible` is run according to the same
[Governance](https://github.com/crossplane/crossplane/blob/master/GOVERNANCE.md)
and [Ownership](https://github.com/crossplane/crossplane/blob/master/OWNERS.md)
structure as the core Crossplane project.

## Code of Conduct

`provider-ansible` adheres to the same [Code of
Conduct](https://github.com/crossplane/crossplane/blob/master/CODE_OF_CONDUCT.md)
as the core Crossplane project.

## Developer guide

`provider-ansible` use [kind](https://github.com/kubernetes-sigs/kind) to run local Kubernetes clusters using Docker container "nodes".

### Run against a Kubernetes cluster

See the header of [`go.mod`](./go.mod) for the minimum supported version of Go.

If you have [go](https://golang.org/doc/devel/release) and [docker](https://www.docker.com/) installed 

```console
make dev
```
is all you need!

clean the dev environment:
```console
make dev-clean
```

## Additional documents

- [`GO`](https://tecadmin.net/install-go-on-debian/): install go1.19+ on debian
- [`DOCKER`](https://docs.docker.com/engine/install/debian/): install docker on debian

## Namespaced resources (Crossplane v2)

The provider serves two API groups.

| Group | Scope | Use it when |
| --- | --- | --- |
| `ansible.crossplane.io` | cluster | You are on Crossplane v1, or composing from a cluster-scoped composite resource. |
| `ansible.m.crossplane.io` | namespaced | You are on Crossplane v2 and composing from a namespaced composite resource. |

A namespaced `AnsibleRun` inherits its namespace from the composite resource
that creates it, which is what Crossplane v2 requires: a namespaced composite
cannot compose a cluster-scoped resource.

Both groups accept the same `spec.forProvider`, so moving a manifest across is a
matter of changing `apiVersion` and adding a namespace. There is no automatic
migration. CRD scope is immutable, so existing cluster-scoped `AnsibleRun`
objects stay where they are and you create namespaced ones alongside them.

See [`examples/namespaced`](examples/namespaced) for a namespaced `AnsibleRun`,
`ProviderConfig`, and `ClusterProviderConfig`.

### Trust model

The namespace confinement described below prevents *accidental*
cross-namespace Secret references. It is not a security boundary, and it does
not make a namespaced `AnsibleRun` safe to hand to an untrusted tenant.

A namespaced `AnsibleRun`'s `playbookInline` is tenant-authored code that
executes inside the provider pod, using whatever ServiceAccount the provider
runs as - typically one with cluster-wide read access to Secrets and other
resources. Anyone who can create an `AnsibleRun` in any namespace can write an
Ansible task that calls the Kubernetes API directly (with the `kubernetes.core`
collection, `curl` against the API server, or the mounted ServiceAccount token)
and read Secrets anywhere in the cluster, bypassing the namespace confinement
entirely. The same is true of `source: Filesystem` and `source: Environment`
credentials and inventories, which read the provider pod's own filesystem and
environment rather than anything namespace-scoped.

In short: a namespaced `AnsibleRun` is as privileged as the provider pod
itself, because it runs arbitrary Ansible there. Permission to create
`AnsibleRun` resources - in any namespace, cluster-scoped or namespaced -
should be treated as equivalent to cluster-admin-adjacent trust, and must not
be granted to untrusted tenants. This is a pre-existing property of running
arbitrary playbooks; the namespaced API does not introduce it, and namespacing
`AnsibleRun` does not change it.

### Choosing a provider config

`spec.providerConfigRef.kind` selects which kind of config to use, and defaults
to `ClusterProviderConfig`:

- `ClusterProviderConfig` - cluster-scoped, shared by every namespace.
- `ProviderConfig` - namespaced, read from the `AnsibleRun`'s own namespace.

### Secrets are namespace-local

A namespaced `AnsibleRun` may only read Secrets in its own namespace, and so
may a namespaced `ProviderConfig`. `secretRef.namespace` is a required field on
every credential and inventory selector - the API server rejects a manifest
that leaves it out - and for these namespaced resources it must be set to the
resource's own namespace; naming any other namespace is refused with a clear
error rather than silently read.

This covers everything the namespaced objects themselves declare:

- an `AnsibleRun`'s `spec.forProvider.inventories`, and
- a namespaced `ProviderConfig`'s `spec.credentials`.

A cluster-scoped `ClusterProviderConfig`'s own `spec.credentials` are the one
exception - see below.

### ClusterProviderConfig credentials are cluster-wide

A `ClusterProviderConfig` is cluster-scoped, so its `secretRef.namespace` may
name any namespace, and the credential resolves for a namespaced `AnsibleRun`
in *every* namespace. One `ClusterProviderConfig` whose credential says
`secretRef.namespace: crossplane-system` therefore serves the whole cluster,
which is what makes it usable as the default `providerConfigRef.kind`. You do
not need to copy the Secret into each namespace.

The relaxation is deliberately narrow. It applies only to the credentials the
cluster-scoped config itself declares. It does not relax anything a tenant
writes in their own namespace: a namespaced `AnsibleRun`'s
`spec.forProvider.inventories` stay confined to the `AnsibleRun`'s namespace
even when it references a `ClusterProviderConfig`, and a namespaced
`ProviderConfig`'s credentials stay confined to its own namespace.

The reasoning follows from "Trust model" above: namespace confinement is
defence-in-depth against *accidental* cross-namespace references, not a
security boundary. A tenant's `playbookInline` already executes in the provider
pod with its cluster-wide ServiceAccount token and can read any Secret it likes
regardless of which config it declares. Confining a cluster-scoped config's
credentials therefore cost a great deal of usability - a cluster-scoped config
that could only ever serve one namespace - for protection a determined tenant
already bypasses. Writing a `ClusterProviderConfig` is an administrator action;
referencing one does not widen what a tenant could already reach.

If you would rather each namespace kept its own credential anyway, give each
one a namespaced `ProviderConfig` and set `providerConfigRef.kind:
ProviderConfig`; those credentials remain confined to their namespace.

### Deletion runs the playbook

The cluster-scoped `AnsibleRun` forces `spec.deletionPolicy: Orphan`, so
deleting it never runs the playbook. The namespaced spec has no
`deletionPolicy` field, so deleting a namespaced `AnsibleRun` runs the
playbook once more with `state: absent` before the object goes away. To keep
the old behaviour, drop the `Delete` action:

    spec:
      managementPolicies: ["Observe", "Create", "Update", "LateInitialize"]

### Upgrading

Upgrading to Crossplane runtime v2 removed `spec.publishConnectionDetailsTo`
from the cluster-scoped `AnsibleRun` CRD, because Crossplane removed External
Secret Stores upstream. This provider never implemented that field, so no
functionality is lost, but a manifest that still sets it will be rejected by
the API server after you upgrade. Remove the field from any existing
`AnsibleRun` manifests before or during the upgrade.

## Licensing

provider-ansible is under the Apache 2.0 license.