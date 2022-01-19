/*
Copyright 2017 The Kubernetes Authors.

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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Foo is a specification for a Foo resource
type Addon struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AddonSpec   `json:"spec"`
	Status AddonStatus `json:"status"`
}

type Status string

const (
	Installing  Status = "Installing"
	Failed      Status = "Failed"
	Upgrading   Status = "Upgrading"
	Deployed    Status = "Deployed"
	Available   Status = "Available"
	Unavailable Status = "Unavailable"
	Unknown     Status = "Unknown"
)

// FooSpec is the spec for a Foo resource
type AddonSpec struct {
	ReadinessProbe      *ReadinessProbe `json:"readinessProbe"`
	Version             string          `json:"version" bson:"version"`
	ChartAddr           string          `json:"chartAddr"`
	AddonTemplateName   string          `json:"addonTemplateName" bson:"addonTemplateName"`
	AddonTemplateType   string          `json:"addonTemplateType" bson:"addonTemplateType"`
	AddonTemplateLogo   string          `json:"addonTemplateLogo" bson:"addonTemplateLogo"`
	Description         string          `json:"description" bson:"description"`
	AddonTemplateLabels []string        `json:"addonTemplateLabels" bson:"addonTemplateLabels"`
	Values              string          `json:"values" bson:"values"`
	BasicValues         string          `json:"basicValues"`
}

// FooStatus is the status for a Foo resource
type AddonStatus struct {
	Status         Status   `json:"status" bson:"status"`
	Reason         string   `json:"reason" bson:"reason"`
	Message        string   `json:"message" bson:"message"`
	TargetVersions []string `json:"targetVersions" bson:"targetVersions"`
	CurrentVersion string   `json:"currentVersion" bson:"currentVersion"`
	CurrentValues  string   `json:"currentValues"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// FooList is a list of Foo resources
type AddonList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Addon `json:"items"`
}

type ReadinessProbe struct {
	Targets []TargetResource `json:"targets"`
}

type TargetResource struct {
	Resource string `json:"resource"`
	Name     string `json:"name"`
}
