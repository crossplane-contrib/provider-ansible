package v1alpha1

import (
	"testing"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
)

func TestAnsibleRunIsModernManaged(t *testing.T) {
	var _ resource.ModernManaged = &AnsibleRun{}
}

func TestProviderConfigsAreProviderConfigs(t *testing.T) {
	var _ resource.ProviderConfig = &ProviderConfig{}
	var _ resource.ProviderConfig = &ClusterProviderConfig{}
}
