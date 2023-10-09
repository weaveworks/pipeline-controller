package leveltriggered

import (
	"context"
	"fmt"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
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
func (r *PipelineReconciler) requestsForCluster(indexKey string) func(obj client.Object) []reconcile.Request {
	return func(obj client.Object) []reconcile.Request {
		ctx := context.Background()
		var list v1alpha1.PipelineList
		key := fmt.Sprintf("%s/%s", obj.GetNamespace(), obj.GetName())
		if err := r.List(ctx, &list, client.MatchingFields{
			indexKey: key,
		}); err != nil {
			return nil
		}

		if len(list.Items) == 0 {
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

const applicationKey = ".spec.environments[].targets[].appRef"

// indexApplication extracts all the application refs from a pipeline. The index keys are
// `<group>/<kind>/<namespace>/<name>`.
func (r *PipelineReconciler) indexApplication(o client.Object) []string {
	p, ok := o.(*v1alpha1.Pipeline)
	if !ok {
		panic(fmt.Sprintf("Expected a Pipeline, got %T", o))
	}

	// TODO future: account for the name being provided in the target ref.
	name := p.Spec.AppRef.Name
	kind := p.Spec.AppRef.Kind
	apiVersion := p.Spec.AppRef.APIVersion
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		// FIXME: ideally we'd log this problem here; but, the log is not available.
		return nil
	}

	var res []string
	for _, env := range p.Spec.Environments {
		for _, target := range env.Targets {
			namespace := target.Namespace
			if namespace == "" {
				namespace = p.GetNamespace()
			}
			key := fmt.Sprintf("%s/%s/%s/%s", gv.Group, kind, namespace, name)
			res = append(res, key)
		}
	}
	return res
}

// requestsForApplication is given an application object, and looks up the pipeline(s) that use it as a target,
// assuming they are indexed using indexApplication (or something using the same key format and index).
func (r *PipelineReconciler) requestsForApplication(obj client.Object) []reconcile.Request {
	ctx := context.Background()
	var list v1alpha1.PipelineList
	gvk, err := apiutil.GVKForObject(obj, r.Scheme)
	if err != nil {
		// FIXME: it'd be good to log here, would require saving a logger in the reconciler, or having it in the closure.
		return nil
	}

	key := fmt.Sprintf("%s/%s/%s/%s", gvk.Group, gvk.Kind, obj.GetNamespace(), obj.GetName())
	if err := r.List(ctx, &list, client.MatchingFields{
		applicationKey: key,
	}); err != nil {
		return nil
	}

	if len(list.Items) == 0 {
		return nil
	}

	reqs := make([]reconcile.Request, len(list.Items))
	for i := range list.Items {
		reqs[i].Name = list.Items[i].Name
		reqs[i].Namespace = list.Items[i].Namespace
	}
	return reqs
}
