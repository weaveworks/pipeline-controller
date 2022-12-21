package v1alpha1

import (
	"fmt"
)

// LocalAppReference is used together with a Target to find a single instance of an application on a certain cluster.
type LocalAppReference struct {
	// API version of the referent.
	// +required
	APIVersion string `json:"apiVersion"`

	// Kind of the referent.
	// +required
	Kind string `json:"kind"`

	// Name of the referent.
	// +required
	Name string `json:"name"`
}

// CrossNamespaceClusterReference contains enough information to let you locate the
// typed Kubernetes resource object at cluster level.
type CrossNamespaceClusterReference struct {
	// API version of the referent.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`

	// Kind of the referent.
	// +kubebuilder:validation:Enum=GitopsCluster
	// +required
	Kind string `json:"kind"`

	// Name of the referent.
	// +required
	Name string `json:"name"`

	// Namespace of the referent, defaults to the namespace of the Kubernetes resource object that contains the reference.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

func (s *CrossNamespaceClusterReference) String() string {
	if s.Namespace != "" {
		return fmt.Sprintf("%s/%s/%s", s.Kind, s.Namespace, s.Name)
	}
	return fmt.Sprintf("%s/%s", s.Kind, s.Name)
}
