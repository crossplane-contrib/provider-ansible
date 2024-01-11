/*
Copyright 2020 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Role is definition of Ansible content role
type Role struct {
	Name string `json:"name"`
	Src  string `json:"src"`
	// +optional
	Version string `json:"version,omitempty"`
}

// AnsibleRunParameters are the configurable fields of a AnsibleRun.
type AnsibleRunParameters struct {
	// The inline inventory of this AnsibleRun; the content of inventory file may be written inline.
	// +optional
	InventoryInline *string `json:"inventoryInline"`

	// The Inventories of this AnsibleRun.
	// +optional
	Inventories []Inventory `json:"inventories"`

	// This sets the Inventory to executable for use by ansible.builtin.script plugin
	// +kubebuilder:default=false
	// +optional
	ExecutableInventory bool `json:"executableInventory"`

	// The inline configuration of this AnsibleRun;  the content of a simple playbook.yml file may be written inline.
	// This field is mutually exclusive with the “roles” field.
	// +optional
	PlaybookInline *string `json:"playbookInline"`

	// The remote configuration of this AnsibleRun; the content can be retrieved from Ansible Galaxy as community contents
	// This field is mutually exclusive with the “Playbooks” and/or "PlaybookInline" fields.
	// +optional
	Roles []Role `json:"roles"`

	// Configuration variables.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Vars runtime.RawExtension `json:"vars,omitempty"`
}

// Inventory required to configure ansible inventory.
type Inventory struct {
	// Source of the inventory.
	// +kubebuilder:validation:Enum=None;Secret;InjectedIdentity;Environment;Filesystem
	Source xpv1.CredentialsSource `json:"source"`

	xpv1.CommonCredentialSelectors `json:",inline"`
}

// AnsibleRunObservation are the observable fields of a AnsibleRun.
type AnsibleRunObservation struct {
	// TODO(negz): Should we include outputs here? Or only in connection
	// details.
}

// A AnsibleRunSpec defines the desired state of a AnsibleRun.
type AnsibleRunSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       AnsibleRunParameters `json:"forProvider"`
}

// A AnsibleRunStatus represents the observed state of a AnsibleRun.
type AnsibleRunStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          AnsibleRunObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// AnsibleRun represents a set of Ansible Playbooks.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Cluster
type AnsibleRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AnsibleRunSpec   `json:"spec"`
	Status AnsibleRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AnsibleRunList is a collection of AnsibleRun.
type AnsibleRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AnsibleRun `json:"items"`
}
