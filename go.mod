module github.com/crossplane/provider-ansible

go 1.16

require (
	github.com/apenella/go-ansible v1.1.4
	github.com/crossplane/crossplane-runtime v0.15.0
	github.com/crossplane/crossplane-tools v0.0.0-20210320162312-1baca298c527
	github.com/google/go-cmp v0.5.6
	github.com/hashicorp/go-getter v1.4.0
	github.com/pkg/errors v0.9.1
	github.com/spf13/afero v1.6.0
	gopkg.in/alecthomas/kingpin.v2 v2.2.6
	k8s.io/apimachinery v0.22.2
	k8s.io/client-go v0.22.2
	sigs.k8s.io/controller-runtime v0.9.6
	sigs.k8s.io/controller-tools v0.6.2
)
