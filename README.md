# Pipeline Controller

The pipeline-controller is a Kubernetes controller offering an API (in terms of the `Pipeline` CRD) and automation for implementing continuous delivery (CD) pipelines in an unopinionated fashion. The API allows for defining a pipeline comprised of multiple environments and deployment targets, typically different clusters serving different purposes, e.g. "dev", "staging", "prod". The controller then tracks applications deployed to those environments and provides visibility into their progress while they make their way through the environments.

## Pipelines

One part of this project is an API to define a continuous delivery pipeline. Please see [the Go types here](api/v1alpha1/pipeline_types.go) for details on this API.

## Promotion

Another part this project offers is an API and machinery for promoting applications through environments. The following image provides an overview of how the promotion flow is implemented (an editable version of this image is maintained in [Miro](https://miro.com/app/board/uXjVPE5kjdU=/?share_link_id=65605735742)):

![Promotion Flow](/docs/img/promotion-flow.jpg)

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

## Contributing

Check out [how to contribute](CONTRIBUTING.md) to the project.
