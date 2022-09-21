package githubpr

import (
	"fmt"

	"github.com/fluxcd/image-automation-controller/pkg/update"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/kustomize/kyaml/fieldmeta"
	"sigs.k8s.io/kustomize/kyaml/kio"
	"sigs.k8s.io/kustomize/kyaml/setters2"
	"sigs.k8s.io/kustomize/kyaml/yaml"

	"github.com/weaveworks/pipeline-controller/server"
)

const setterShortHand = "$promotion"

func (g GitHubPR) patchManifests(inPath string, promotion server.Promotion) error {
	fieldmeta.SetShortHandRef(setterShortHand)
	pipeline := kio.Pipeline{
		Inputs: []kio.Reader{&update.ScreeningLocalReader{
			Path:  inPath,
			Token: fmt.Sprintf("%q", setterShortHand),
		}},
		Outputs: []kio.Writer{kio.LocalPackageWriter{
			PackagePath: inPath,
		}},
		Filters: []kio.Filter{
			kio.FilterFunc(func(nodes []*yaml.RNode) ([]*yaml.RNode, error) {
				var schema spec.Schema
				setterSchema := spec.StringProperty()
				setterSchema.Extensions = spec.Extensions{}
				setterSchema.Extensions.Add(setters2.K8sCliExtensionKey, map[string]interface{}{
					"setter": map[string]string{
						"name":  "version",
						"value": promotion.Version,
					},
				})
				schema.Definitions = spec.Definitions{
					fieldmeta.SetterDefinitionPrefix + promotion.AppNS + ":" + promotion.AppName + ":" + promotion.Environment.Name: *setterSchema,
				}
				set := setters2.Set{
					Name:          "version",
					SettersSchema: &schema,
				}
				for idx := range nodes {
					_, err := set.Filter(nodes[idx])
					if err != nil {
						return nil, fmt.Errorf("failed to apply filter: %w", err)
					}
				}
				return nodes, nil
			}),
		},
	}

	if err := pipeline.Execute(); err != nil {
		return fmt.Errorf("failed to execute pipeline: %w", err)
	}

	return nil
}
