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
	// DefaultRequeueInterval is used when immediate re-queueing of a reconcile request isn't necessary, e.g. when it's expected to be
	// triggered by a watched resource before.
	DefaultRequeueInterval = 30 * time.Minute
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="App Kind",type="string",JSONPath=".spec.appRef.kind",description=""
// +kubebuilder:printcolumn:name="App Name",type="string",JSONPath=".spec.appRef.name",description=""
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
	// Environments is a list of environments to which the pipeline's application is supposed to be deployed.
	// +required
	Environments []Environment `json:"environments"`
	// AppRef denotes the name and type of the application that's governed by the pipeline.
	// +required
	AppRef LocalAppReference `json:"appRef"`
}

type PipelineStatus struct {
	// ObservedGeneration is the last observed generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions holds the conditions for the Pipeline.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// The last successfully applied revision.
	// The revision format for Git sources is <branch|tag>/<commit-sha>.
	// +optional
	LastAppliedRevision string `json:"lastAppliedRevision,omitempty"`
}

type Environment struct {
	// Name defines the name of this environment. This is commonly something such as "dev" or "prod".
	// +required
	Name string `json:"name"`
	// Targets is a list of targets that are part of this environment. Each environment should have
	// at least one target.
	// +required
	Targets []Target `json:"targets"`
}

type Target struct {
	// Namespace denotes the namespace of this target on the referenced cluster. This is where
	// the app pointed to by the environment's `appRef` is searched.
	// +required
	Namespace string `json:"namespace"`
	// ClusterRef points to the cluster that's targeted by this target. If this field is not set, then the target is assumed
	// to point to a Namespace on the cluster that the Pipeline resources resides on (i.e. a local target).
	// +optional
	ClusterRef *CrossNamespaceClusterReference `json:"clusterRef,omitempty"`
}

func (t Target) String() string {
	return fmt.Sprintf("%s_%s", t.ClusterRef.String(), t.Namespace)
}

func init() {
	SchemeBuilder.Register(&Pipeline{}, &PipelineList{})
}
