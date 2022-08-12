package controllers

import (
	"context"
	"fmt"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func (r *PipelineReconciler) indexBy(kind string) func(o client.Object) []string {
	return func(o client.Object) []string {
		p, ok := o.(*v1alpha1.Pipeline)
		if !ok {
			panic(fmt.Sprintf("Expected a Pipeline, got %T", o))
		}

		var res []string
		for _, env := range p.Spec.Environments {
			for _, target := range env.Targets {
				if target.ClusterRef.Kind == kind {
					namespace := p.GetNamespace()
					if target.ClusterRef.Namespace != "" {
						namespace = target.ClusterRef.Namespace
					}
					res = append(res, fmt.Sprintf("%s/%s", namespace, target.ClusterRef.Name))
				}
			}
		}

		return res
	}
}

func (r *PipelineReconciler) requestsForRevisionChangeOf(indexKey string) func(obj client.Object) []reconcile.Request {
	return func(obj client.Object) []reconcile.Request {
		ctx := context.Background()
		var list v1alpha1.PipelineList
		if err := r.List(ctx, &list, client.MatchingFields{
			indexKey: client.ObjectKeyFromObject(obj).String(),
		}); err != nil {
			return nil
		}
		var dd []*v1alpha1.Pipeline
		for _, d := range list.Items {
			dd = append(dd, d.DeepCopy())
		}
		reqs := make([]reconcile.Request, len(dd))
		for i := range dd {
			reqs[i].NamespacedName.Name = dd[i].Name
			reqs[i].NamespacedName.Namespace = dd[i].Namespace
		}
		return reqs
	}
}
