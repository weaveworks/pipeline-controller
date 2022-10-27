package noop

import (
	"context"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

type Noop struct{}

var (
	_ strategy.Strategy = Noop{}
)

func NewNoop() (*Noop, error) {
	return &Noop{}, nil
}

func (g Noop) Handles(p pipelinev1alpha1.Promotion) bool {
	return p.Noop != nil
}

func (g Noop) Promote(ctx context.Context, promSpec pipelinev1alpha1.Promotion, promotion strategy.Promotion) (*strategy.PromotionResult, error) {
	return &strategy.PromotionResult{}, nil
}
