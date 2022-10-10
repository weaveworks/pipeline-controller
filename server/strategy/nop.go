package strategy

import (
	"context"
)

type Nop struct{}

func (n Nop) Promote(_ context.Context, _ Promotion) (*PromotionResult, error) {
	return &PromotionResult{}, nil
}
