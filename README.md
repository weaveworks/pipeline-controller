# Pipeline Controller

The pipeline-controller is a Kubernetes controller offering an API (in terms of the `Pipeline` CRD) and automation for implementing continuous delivery (CD) pipelines in an unopinionated fashion. The API allows for defining a pipeline comprised of multiple environments and deployment targets, typically different clusters serving different purposes, e.g. "dev", "staging", "prod". The controller then tracks applications deployed to those environments and provides visibility into their progress while they make their way through the environments.

## Getting Started

1. Install the CRD on your cluster:
   ```sh
   make install
   ```
2. Run the controller:
   ```sh
   make run
   ```

## Create example pipeline

To make it easier to develop on the controller, you can add example pipeline
resources:

```bash
# Github
kubectl apply --recursive -f e2e/testdata/pipelines/github/

# Gitlab
kubectl apply --recursive -f e2e/testdata/pipelines/gitlab/
```

## Pipeline Templates

You could manage [gitops templates](https://docs.gitops.weave.works/docs/gitops-templates/templates/) for pipelines. 
They aim to simplify and enhace the pipeline creation experience. Existing templates for pipelines
are packaged as helm chart [pipeline-templates](./charts/pipeline-templates) and integrated with [pipeline controller](./charts/pipeline-controller) 
via feature flag `.values.templates.enabled`.

For guidance using it see [pipeline templates user documentation](https://docs.gitops.weave.works/docs/pipelines/pipeline-templates/).

## Contributing

Check out [how to contribute](CONTRIBUTING.md) to the project.
