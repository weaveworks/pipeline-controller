package v1alpha1

import (
	"fmt"
	"time"

	"github.com/fluxcd/pkg/apis/meta"
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
	// Promotion defines details about how promotions are carried out between the environments
	// of this pipeline.
	// +optional
	Promotion *Promotion `json:"promotion,omitempty"`
}

// GetPromotion returns the environment promotion if set, otherwise returns the default promotion..
func (ps PipelineSpec) GetPromotion(env string) *Promotion {
	for _, e := range ps.Environments {
		if e.Name == env && e.Promotion != nil {
			return e.Promotion
		}
	}

	return ps.Promotion
}

// Promotion define promotion configuration for the pipeline.
type Promotion struct {
	// Manual option to allow promotion between to require manual approval before proceeding.
	// +optional
	Manual bool `json:"manual,omitempty"`
	// Strategy defines which strategy the promotion should use.
	Strategy Strategy `json:"strategy"`
}

// Strategy defines all the available promotion strategies. All of the fields in here are mutually exclusive, i.e. you can only select one
// promotion strategy per Pipeline. Failure to do so will result in undefined behaviour.
type Strategy struct {
	// PullRequest defines a promotion through a Pull Request.
	// +optional
	PullRequest *PullRequestPromotion `json:"pull-request,omitempty"`
	// Notification defines a promotion where an event is emitted through Flux's notification-controller each time an app is to be promoted.
	// +optional
	Notification *NotificationPromotion `json:"notification,omitempty"`
	// SecrefRef reference the secret that contains a 'hmac-key' field with HMAC key used to authenticate webhook calls.
	// +optional
	SecretRef *meta.LocalObjectReference `json:"secretRef,omitempty"`
}

type GitProviderType string

const (
	Github          GitProviderType = "github"
	Gitlab          GitProviderType = "gitlab"
	BitBucketServer GitProviderType = "bitbucket-server"
)

func (g GitProviderType) String() string {
	return string(g)
}

type PullRequestPromotion struct {
	// Indicates the git provider type to manage pull requests.
	// +required
	// +kubebuilder:validation:Enum=github;gitlab;bitbucket-server
	Type GitProviderType `json:"type"`
	// The git repository HTTPS URL used to patch the manifests for promotion.
	// +required
	URL string `json:"url"`
	// The branch to checkout after cloning. Note: This is just the base
	// branch that will eventually receive the PR changes upon merge and does
	// not denote the branch used to create a PR from. The latter is generated
	// automatically and cannot be provided.
	// +required
	BaseBranch string `json:"baseBranch"`
	// SecretRef specifies the Secret containing authentication credentials for
	// the git repository and for the git provider API.
	// For HTTPS repositories the Secret must contain 'username' and 'password'
	// fields.
	// For Git Provider API to manage pull requests, it must contain a 'token' field.
	// +required
	SecretRef meta.LocalObjectReference `json:"secretRef"`
}

type NotificationPromotion struct{}

type PipelineStatus struct {
	// ObservedGeneration is the last observed generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions holds the conditions for the Pipeline.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Environments holds environment statuses.
	// +optional
	Environments map[string]*EnvironmentStatus `json:"environments"`
}

// GetWaitingApproval returns the waiting approval of an environment.
func (p *PipelineStatus) GetWaitingApproval(env string) WaitingApproval {
	val, ok := p.Environments[env]
	if !ok {
		return WaitingApproval{}
	}

	return val.WaitingApproval
}

// SetWaitingApproval sets the waiting approval of a environment.
func (p *PipelineStatus) SetWaitingApproval(env, revision string) {
	waitingApproval := WaitingApproval{
		Revision: revision,
	}

	p.setWaitingApproval(env, waitingApproval)
}

// ResetWaitingApproval resets the waiting approval of an environment.
func (p *PipelineStatus) ResetWaitingApproval(env string) {
	p.setWaitingApproval(env, WaitingApproval{})
}

func (p *PipelineStatus) setWaitingApproval(env string, waitingApproval WaitingApproval) {
	if p.Environments == nil {
		p.Environments = make(map[string]*EnvironmentStatus)
	}

	val, ok := p.Environments[env]
	if !ok {
		p.Environments[env] = &EnvironmentStatus{
			WaitingApproval: waitingApproval,
		}

		return
	}

	val.WaitingApproval = waitingApproval
}

type EnvironmentStatus struct {
	WaitingApproval WaitingApproval `json:"waitingApproval,omitempty"`
}

// WaitingApproval holds the environment revision that's currently waiting approval.
type WaitingApproval struct {
	// Revision waiting approval.
	Revision string `json:"revision"`
}

type Environment struct {
	// Name defines the name of this environment. This is commonly something such as "dev" or "prod".
	// +required
	Name string `json:"name"`
	// Targets is a list of targets that are part of this environment. Each environment should have
	// at least one target.
	// +required
	Targets []Target `json:"targets"`

	// Promotion defines details about how the promotion is done on this environment.
	// +optional
	Promotion *Promotion `json:"promotion,omitempty"`
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
