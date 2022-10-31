package notification

import (
	"context"
	"fmt"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/server/strategy"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kuberecorder "k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	PromoteReason = "Promote"
)

type Notification struct {
	kubeClient    client.Client
	eventRecorder kuberecorder.EventRecorder
}

var (
	_ strategy.Strategy = Notification{}
)

func NewNotification(kubeClient client.Client, eventRecorder kuberecorder.EventRecorder) (*Notification, error) {
	return &Notification{
		kubeClient:    kubeClient,
		eventRecorder: eventRecorder,
	}, nil
}

func (g Notification) Handles(p pipelinev1alpha1.Promotion) bool {
	return p.Notification != nil
}

func (g Notification) Promote(ctx context.Context, promSpec pipelinev1alpha1.Promotion, promotion strategy.Promotion) (*strategy.PromotionResult, error) {
	p := &pipelinev1alpha1.Pipeline{
		ObjectMeta: v1.ObjectMeta{
			Name:      promotion.PipelineName,
			Namespace: promotion.PipelineNamespace,
		},
	}

	if err := g.kubeClient.Get(ctx, client.ObjectKeyFromObject(p), p); err != nil {
		return nil, fmt.Errorf("failed getting pipeline=%s/%s: %w", promotion.PipelineNamespace, promotion.PipelineName, err)
	}

	metadata := map[string]string{
		"pipelineName":      promotion.PipelineName,
		"pipelineNamespace": promotion.PipelineNamespace,
		"environment":       promotion.Environment.Name,
		"version":           promotion.Version,
	}

	g.eventRecorder.AnnotatedEventf(p, metadata, corev1.EventTypeNormal, PromoteReason,
		"Promote pipeline %s/%s to %s with version %s", promotion.PipelineNamespace, promotion.PipelineName, promotion.Environment.Name, promotion.Version)

	return &strategy.PromotionResult{}, nil
}
