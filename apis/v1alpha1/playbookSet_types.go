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

// A ConfigurationSource represents the source of a PlaybookSet Configuration.
// +kubebuilder:validation:Enum=Remote;Inline
type ConfigurationSource string

// Module sources.
const (
	ConfigurationSourceRemote ConfigurationSource = "Remote"
	ConfigurationSourceInline ConfigurationSource = "Inline"
)

// PlaybookSetParameters are the configurable fields of a PlaybookSet.
type PlaybookSetParameters struct {
	// The configuration of this PlaybookSet; i.e. the configuration containing its playbook
	// files. When the playbookSet's Provider source is 'Remote' (the default) this can be
	// any address supported by Ansible.Builtin.git,
	// TODO support other remotes https://docs.ansible.com/ansible/latest/collections/ansible/builtin/index.html
	// When the playbookSet's source is 'Inline' the
	// content of a simple playbook.yml file may be written inline.
	Module string `json:"module"`

	// Source of configuration of this playbookSet.
	Source ConfigurationSource `json:"source"`
}

// PlaybookSetObservation are the observable fields of a PlaybookSet.
type PlaybookSetObservation struct {
	// TODO(negz): Should we include outputs here? Or only in connection
	// details.
}

// A PlaybookSetSpec defines the desired state of a PlaybookSet.
type PlaybookSetSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       PlaybookSetParameters `json:"forProvider"`
}

// A PlaybookSetStatus represents the observed state of a PlaybookSet.
type PlaybookSetStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          PlaybookSetObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// PlaybookSet represents a set of Ansible Playbooks.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
type PlaybookSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PlaybookSetSpec   `json:"spec"`
	Status PlaybookSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PlaybookSetList is a collection of PlaybookSet.
type PlaybookSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PlaybookSet `json:"items"`
}
