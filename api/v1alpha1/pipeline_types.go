package v1alpha1

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// PipelineKind is the string representation of a Pipeline.
	PipelineKind = "Pipeline"
	// MaxConditionMessageLength denotes the maximum length of the `.status.conditions.message` field.
	MaxConditionMessageLength = 20000
	DefaultRequeueInterval    = 30 * time.Minute
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].message",description=""
// Pipeline is the Schema for the pipelines API
type Pipeline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec PipelineSpec `json:"spec,omitempty"`
	// +kubebuilder:default={"observedGeneration":-1}
	Status PipelineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// PipelineList contains a list of Pipelines
type PipelineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Pipeline `json:"items"`
}

type PipelineSpec struct {
	// +required
	Environments []Environment `json:"environments"`
}

type Environment struct {
	// Name name of the environment.
	// +required
	Name string `json:"name"`
	// Targets list of target that consists of combination of Namespace and Cluster.
	Targets []Target `json:"targets"`
}

type Target struct {
	// ClusterRef references a cluster.
	// +required
	ClusterRef CrossNamespaceSourceReference `json:"clusterRef"`
	// Namespace namespace where the ClusterRef is defined.
	// +required
	Namespace string `json:"namespace"`
}

type PipelineStatus struct {
	// ObservedGeneration is the last observed generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions holds the conditions for the Pipeline.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Environments holds the current status of all environments.
	// +optional
	Environments []EnvironmentStatus `json:"environments"`
}

type EnvironmentStatus struct {
	// Name environment name.
	// +required
	Name string `json:"name"`

	// TargetsStatus status of all targets defined in this Environment.
	TargetsStatus []TargetStatus `json:"targetsStatus,omitempty"`
}

type TargetStatus struct {
	// ClusterRef references a cluster.
	// +required
	ClusterRef CrossNamespaceSourceReference `json:"clusterRef"`
	// Namespace namespace where the ClusterRef is defined.
	// +required
	Namespace string `json:"namespace"`
	// Workloads holds helm releases and kustomization that belongs to this target.
	// +optional
	Workloads []CrossNamespaceSourceReference `json:"workloads,omitempty"`
}

func (t Target) String() string {
	return fmt.Sprintf("%s_%s", t.ClusterRef.String(), t.Namespace)
}

func init() {
	SchemeBuilder.Register(&Pipeline{}, &PipelineList{})
}
