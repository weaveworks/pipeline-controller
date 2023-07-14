# Pipeline Controller

The pipeline-controller is a Kubernetes controller offering an API (in terms of the `Pipeline` CRD) and automation for implementing continuous delivery (CD) pipelines in an unopinionated fashion. The API allows for defining a pipeline comprised of multiple environments and deployment targets, typically different clusters serving different purposes, e.g. "dev", "staging", "prod". The controller then tracks applications deployed to those environments and provides visibility into their progress while they make their way through the environments.

## Pipeline CRD

One part of this project is an API to define a continuous delivery pipeline. Please see [the Go types here](api/v1alpha1/pipeline_types.go) for details on this API.

## Components

The current solution implements a push model. The controller does not poll the target enviroments at regular intervals to determine if a deployment has taken place. Instead, it exposes an endpoint that the target environment needs to call, in order to notify the controller of a new deployment. Following that, the controller will lookup the next environment that needs to get updated from the Pipeline CR and initiate a promotion to that environment. There are two types of promotions (strategies) supported right now:
- Promotion via a pull request: the controller creates a pull request to update the next environment. The pull request needs to be reviewed by a human and once merged, Flux (which is running on the target environment) will deploy the application. 
- Promotion via notification: the controller will send a notification that includes promotion information (environment/version) to the notification controller running on the same cluster. The notification controller will then forward this notification to another system (for example a GitHub Action) that will do the actual promotion.

For more details on the mechanics of promotions, please refer to the [promotion](#promotion) section below.

For an overview of all the components needed for a pipeline to work, please refer to the image below.

![pipeline components](/docs/img/pipeline-components.png)

### Limitations
- The current solution expects all the target environments to be specified in the Pipeline CR. Because of this, pipelines that need to target hundreds of environments require a lot of effort from the user in order to keep the CR up to date.
- The first environment of the pipeline is expected to be setup already before any promotions take place. In fact the pipelines feature as a whole does very little to help the user establish a full CD pipeline. A better approach would be for the user to describe **what** and **where** and the pipeline controller to take care of the rest across **all** environments, including the first one.
- There is a lot of additional overhead from the user's percpective to ensure that everything is in place for a pipeline to work:
  - the endpoint needs to be publicly exposed via an ingress route in order to be reachable by leaf clusters
  - there is a need for an Alert/Provider resource to be present in each target environment in order to be able to notify the pipeline controller of a new deployment
  - for the pull request strategy, the user needs to put the markers in the “right” files or the promotion doesn’t work

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
