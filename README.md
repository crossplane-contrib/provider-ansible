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
Crossplane [document](https://crossplane.io/docs/).

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

If you have [go (1.19+)](https://golang.org/doc/devel/release.html#policy) and [docker](https://www.docker.com/) installed 

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

## Licensing

provider-ansible is under the Apache 2.0 license.
