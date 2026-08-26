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
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	shared "github.com/crossplane-contrib/provider-ansible/apis/shared/v1alpha1"
)

// A AnsibleRunSpec defines the desired state of a AnsibleRun.
type AnsibleRunSpec struct {
	xpv2.ClusterManagedResourceSpec `json:",inline"`
	ForProvider                     shared.AnsibleRunParameters `json:"forProvider"`
}

// A AnsibleRunStatus represents the observed state of a AnsibleRun.
type AnsibleRunStatus struct {
	xpv2.ManagedResourceStatus `json:",inline"`
	AtProvider                 shared.AnsibleRunObservation `json:"atProvider,omitempty"`
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
