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
func (r *PipelineReconciler) requestsForCluster(indexKey string) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
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

// targetKeyFunc is a type representing a way to get an index key from a target spec.
type targetKeyFunc func(clusterName, clusterNamespace string, targetKind schema.GroupVersionKind, targetName, targetNamespace string) string

// indexTargets is given a func which returns a key for a target spec, and returns a `client.IndexerFunc` that will index
// each target from a Pipeline object.
func indexTargets(fn targetKeyFunc) func(client.Object) []string {
	return func(o client.Object) []string {
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
		gvk := gv.WithKind(kind)

		var res []string
		for _, env := range p.Spec.Environments {
			for _, target := range env.Targets {
				var clusterName, clusterNamespace string
				if target.ClusterRef != nil {
					clusterName = target.ClusterRef.Name
					clusterNamespace = target.ClusterRef.Namespace
					if clusterNamespace == "" {
						clusterNamespace = p.GetNamespace()
					}
				}

				namespace := target.Namespace
				if namespace == "" {
					namespace = p.GetNamespace()
				}
				key := fn(clusterNamespace, clusterName, gvk, namespace, name)
				res = append(res, key)
			}
		}
		return res
	}
}

// indexApplication extracts all the application refs from a pipeline. The index keys are
//
//	<cluster namespace>/<cluster name>:<group>/<kind>/<namespace>/<name>`.
var indexApplication = indexTargets(func(clusterNamespace, clusterName string, gvk schema.GroupVersionKind, targetNamespace, targetName string) string {
	key := fmt.Sprintf("%s/%s:%s/%s/%s/%s", clusterNamespace, clusterName, gvk.Group, gvk.Kind, targetNamespace, targetName)
	return key
})

// pipelinesForApplication is given an application object and its cluster, and looks up the pipeline(s) that use it as a target.
// It assumes applications are indexed using `indexApplication(...)` (or something using the same key format and index).
// `clusterName` can be a zero value, but if it's not, both the namespace and name should be supplied (since the namespace will
// be give a value if there's only a name supplied, when indexing. See `indexApplication()`).
func (r *PipelineReconciler) pipelinesForApplication(clusterName client.ObjectKey, obj client.Object) ([]v1alpha1.Pipeline, error) {
	ctx := context.Background()
	var list v1alpha1.PipelineList
	gvk, err := apiutil.GVKForObject(obj, r.Scheme)
	if err != nil {
		return nil, err
	}

	key := fmt.Sprintf("%s/%s:%s/%s/%s/%s", clusterName.Namespace, clusterName.Name, gvk.Group, gvk.Kind, obj.GetNamespace(), obj.GetName())
	if err := r.List(ctx, &list, client.MatchingFields{
		applicationKey: key,
	}); err != nil {
		return nil, err
	}

	return list.Items, nil
}
