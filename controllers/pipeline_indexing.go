package controllers

import (
	"context"
	"fmt"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// indexClusterKind returns a func that will extract all the cluster
// refs of the kind given, from a pipeline. This func can be supplied
// to `clientCache.IndexField`. The index values are
// `<namespace>/<name>`; if you index more than one kind, use a
// different key.
func (r *PipelineReconciler) indexClusterKind(kind string) func(o client.Object) []string {
	return func(o client.Object) []string {
		p, ok := o.(*v1alpha1.Pipeline)
		if !ok {
			panic(fmt.Sprintf("Expected a Pipeline, got %T", o))
		}

		var res []string
		for _, env := range p.Spec.Environments {
			for _, target := range env.Targets {
				if target.ClusterRef != nil && target.ClusterRef.Kind == kind {
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

// requestsForCluster returns a func that will look up the pipelines
// using a cluster, as indexed by `indexClusterKind`.
func (r *PipelineReconciler) requestsForCluster(indexKey string) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.PipelineList
		key := fmt.Sprintf("%s/%s", obj.GetNamespace(), obj.GetName())
		if err := r.List(ctx, &list, client.MatchingFields{
			indexKey: key,
		}); err != nil {
			return nil
		}

		reqs := make([]reconcile.Request, len(list.Items))
		for i := range list.Items {
			reqs[i].Name = list.Items[i].Name
			reqs[i].Namespace = list.Items[i].Namespace
		}
		return reqs
	}
}
