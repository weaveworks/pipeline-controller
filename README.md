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

## Contributing

Check out [how to contribute](CONTRIBUTING.md) to the project.
