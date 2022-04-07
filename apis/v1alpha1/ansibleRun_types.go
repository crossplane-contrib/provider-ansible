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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

// A ConfigurationSource represents the source of a AnsibleRun Configuration.
// +kubebuilder:validation:Enum=Remote;Inline
type ConfigurationSource string

// Module sources.
const (
	ConfigurationSourceRemote ConfigurationSource = "Remote"
	ConfigurationSourceInline ConfigurationSource = "Inline"
)

// A Var represents an Ansible configuration variable.
type Var struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// AnsibleRunParameters are the configurable fields of a AnsibleRun.
type AnsibleRunParameters struct {
	// The configuration of this AnsibleRun; i.e. the configuration containing its playbook(s)/Role(s)
	// files. When the AnsibleRun's Provider source is 'Remote' (the default) this can be
	// any address supported by Ansible.Builtin.git,
	// TODO support other remotes https://docs.ansible.com/ansible/latest/collections/ansible/builtin/index.html
	// When the AnsibleRun's source is 'Inline' the
	// content of a simple playbook.yml file may be written inline.
	Module string `json:"module"`

	// Source of configuration of this AnsibleRun.
	Source ConfigurationSource `json:"source"`

	// This is the playbook name. This playbook is expected to be simply a way to call roles.
	// This field is mutually exclusive with the “role” field.
	// For remote source, the playbook is expected to be in the remote project directory.
	// this filed has non effect on inline mode
	// +optional
	Playbook string `json:"playbook,omitempty"`

	// Specifies a role to be executed. This field is mutually exclusive with the “playbook” field. For remote source This field can be:
	// - a relative path within the project working directory
	// - a relative path within one of the directories specified by ANSIBLE_ROLES_PATH environment variable or ansible-roles-path flag.
	// this filed has non effect on inline mode
	// +optional
	Role string `json:"role,omitempty"`

	// Configuration variables.
	// +optional
	Vars []Var `json:"vars,omitempty"`
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
