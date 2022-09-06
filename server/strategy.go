package server

import (
	"context"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
)

type Strategy interface {
	Promote(context.Context, Promotion) (*PromotionResult, error)
}

type Promotion struct {
	AppNS       string                       `json:"appNS"`
	AppName     string                       `json:"appName"`
	Environment pipelinev1alpha1.Environment `json:"environment"`
	Version     string                       `json:"version"`
}

type PromotionResult struct {
	Location string
}
