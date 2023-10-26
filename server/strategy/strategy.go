package strategy

import (
	"context"
	"fmt"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
)

//go:generate mockgen -destination mock_strategy_test.go -package strategy github.com/weaveworks/pipeline-controller/server/strategy Strategy

// Strategy is the interface that all types need to implement that intend to handle at least one of the strategies requested in a Pipeline's
// `.spec.promotion` field.
type Strategy interface {
	// Handles is called by the StrategyRegistry to determine if the type is eligible to handle a promotion. Expect a subsequent call to Promote when
	// returning true here.
	Handles(pipelinev1alpha1.Promotion) bool
	// Promote will be called whenever a promotion is requested through the promotion webhook server and this type's Handles method returns true.
	// Note that a non-nil error returned here will not be made visible to the caller of the webhook.
	Promote(context.Context, pipelinev1alpha1.Promotion, Promotion) (*PromotionResult, error)
}

// Promotion is the type encapsulating a single promotion request.
type Promotion struct {
	PipelineNamespace string                       `json:"pipelineNamespace"`
	PipelineName      string                       `json:"pipelineName"`
	Environment       pipelinev1alpha1.Environment `json:"environment"`
	Version           string                       `json:"version"`
}

// PromotionResult is returned by a Strategy and contains data supposed to be passed on to the webhook caller.
type PromotionResult struct {
	// Location, if non-nil, will be used to set the "Location" HTTP header in a response to a webhook request.
	Location string
}

// StrategyRegistry is a list of all the supported promotion strategies.
type StrategyRegistry []Strategy

// Register adds a strategy to the list of supported registries.
func (r *StrategyRegistry) Register(s Strategy) {
	*r = append(*r, s)
}

// Get returns a strategy that is able to handle the given promotion spec. If the registry holds multiple strategies able to handle the promotion
// the first one is chosen. If the promotion spec contains multiple promotion strategies, Get will return the first strategy that is able to handle
// at least one of them.
func (r StrategyRegistry) Get(p pipelinev1alpha1.Promotion) (Strategy, error) {
	for _, s := range r {
		if s.Handles(p) {
			return s, nil
		}
	}
	return nil, fmt.Errorf("no known promotion strategy requested")
}
