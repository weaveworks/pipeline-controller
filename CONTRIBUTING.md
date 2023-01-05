# Contributing

## Building from Source

1. Before building the container image for the controller you will need to create the file `.netrc` in the repository's root directory which is used
by Go to download dependencies from private repositories (replace `USERNAME` with your GitHub username and `PAT` with a [personal access token](https://github.com/settings/tokens) that has at least `repo` scope):

   ```sh
   echo "machine github.com login USERNAME password PAT" > .netrc
   ```

1. Building the image is then a matter of running

   ```sh
   make docker-build
   ```

## Running Tests

The project has unit, integration and end-to-end testing. For all of them we are using `go test` for all test types
(including e2e) due to its simplicity.

### Unit Testing

This project has unit tests

```sh
make test
```

Testing on this controller follows closely what is done by default in `kubebuilder` although we ditched the use of `Gomega` in favor of native Go testing structure. 
As an example on how to write tests for this controller you can take a look at Kubebuilder [docs](https://book.kubebuilder.io/cronjob-tutorial/writing-tests.html#writing-controller-tests) for reference.

### Integration Testing

This project has integration tests that you could run via its target.

```sh
make integration-test
```

#### Environment Configuration 

For git provider integration tests you would need these in your environment.

| Variable        | Description                                                       |
|-----------------|-------------------------------------------------------------------|
| GITHUB_USER     | github username to use in api calls                               |
| GITHUB_TOKEN    | github PAT token to use in api calls                              |
| GITLAB_USER     | gitlab username to use in api calls                               |
| GITLAB_TOKEN    | gitlab PAT token to use in api calls                              |

#### Adding integration tests

An example you could see on [pullrequest_integration_test.go](./server/strategy/pullrequest/pullrequest_integration_test.go)
where you would need to add the build tag `integration`

```go
//go:build integration
```

### End-to-End testing 

This project has a test suite for end to end pipeline controller journeys. You could find them
within [e2e folder](./e2e). 

#### Configuration

A set of [test pipelines](/e2e/testdata/pipelines) exists to run the tests. They depend on a set 
of common resources.

A set of git repos are required to hold the manifests to use during pull request promotion strategy.

- [github repo](https://github.com/weaveworks/pipeline-controller-e2e)
- [gitlab repo](https://gitlab.git.dev.weave.works/pipeline-controller/pipeline-controller-e2e)

A [set of credentials](https://docs.gitops.weave.works/docs/pipelines/promoting-applications/#create-credentials-secret) 
per git provider needs to exist.

```yaml
apiVersion: v1
data:
  password: <my-github-pat>
  token: <my-github-pat>
  username: <my-github-username>
kind: Secret
metadata:
  name: github-promotion-credentials
  namespace: flux-system
type: Opaque
---
apiVersion: v1
data:
  password: <my-gitlab-pat>
  token: <my-gitlab-pat>
  username: <my-gitlab-username>
kind: Secret
metadata:
  name: gitlab-promotion-credentials
  namespace: flux-system
type: Opaque
```
#### Running the tests

Execute `make e2e` to exercise the test.  

In order to interact with them the following targets exists in [Makefile](./Makefile):

- `e2e-setup`: provisions a local environment with kind, flux and pipeline controller to run the tests.
- `e2e-test`: runs the tests against the running environment.
- `e2e-clean`: tears down the created environment.
- `e2e`: single target that executes setup + test + clean targets

One of the concerns with end-to-end testing is that tend to be unstable. A simple script to test how stable
is the test suite could be used [stability script](/hack/e2e-suite-stability.sh).

#### Adding a test

Add your test case within the e2e folder. An example is [promotion_pull_request_test.go](e2e/promotion_pull_request_test.go)

## Working with promotions

In order to test promotion of an application from one environment to another you need to set up several resources:

1. Create a [GitHub personal access token](https://github.com/settings/tokens). This token needs to provide permission to create PRs (i.e. it needs `repo` scope for private repositories). If you intend to use the same token for cloning the Git repository, make sure the permission to do so is also provided by that token, especially when you're using the new fine-grained tokens.

1. Create a Secret containing authentication credentials for cloning your Git repository (see the Pipeline CRD's documentation of the `spec.promotion.pull-request.secretRef` field for the details on this). Example command to create such a Secret (if you have 2FA enabled for your GitHub account you can't use your GitHub password for cloning repos. In that case just use the token you created above and make sure it has permission to clone the repository):

   ```sh
   $ k create secret generic promotion-credentials --from-literal=username=GITHUB_USERNAME --from-literal=password=GITHUB_PASSWORD --from-literal=token=GITHUB_TOKEN --dry-run=client -o yaml
   ```

2. Define a pipeline with at least two environments and a promotion configuration. An example pipeline manifest can be found in [`/config/testdata/pipeline.yaml`](/config/testdata/pipeline.yaml). Note that you can define different environments on the same cluster so all you need is one cluster and use different namespaces on it, one for each environment. Don't forget to set the `spec.promotion.pull-request.secretRef` field to point to the secret you created in the previous step.

3. Create manifests in your Git repo for a namespace for each environment and one HelmRelease for the application you want to use for testing (e.g. podinfo) for each of the namespaces.

4. Add a promotion marker to the HelmRelease's `.spec.chart.spec.version` field to the manifest of the second environment. In the example below a marker for the Pipeline object `podinfo` in the namespace `default` and the target environment `prod` is added.
   ```yaml
   apiVersion: helm.toolkit.fluxcd.io/v2beta1
   kind: HelmRelease
   [...]
   spec:
     chart:
       spec:
         version: 6.2.0 # {"$promotion": "default:podinfo:prod"}
   [...]
   ```

5. Create a `Provider` and an `Alert` for notification-controller to call out to the promotion webhook whenever the version of the first environment is changed. The Provider must look similar to this:

   ```yaml
   apiVersion: notification.toolkit.fluxcd.io/v1beta1
   kind: Provider
   [...]
   spec:
     address: "http://pipeline-promotion.pipeline-system.svc/promotion/default/podinfo/dev"
     type: generic
   [...]
   ```

   The Alert must look like this (make sure to edit the event source and the `providerRef`):

   ```yaml
   apiVersion: notification.toolkit.fluxcd.io/v1beta1
   kind: Alert
   [...]
   spec:
     eventSeverity: info
     eventSources:
     - kind: HelmRelease
       name: podinfo
     exclusionList:
     - .*upgrade.*has.*started
     - .*is.*not.*ready
     - ^Dependencies.*
     providerRef:
       name: promotion-podinfo

6. For testing promotion you can suspend the HelmRelease in the first environment and resume it. This will cause notification-controller to send an event the promotion webhook.

## Releasing

This repository hosts the sources for two artifacts, a container image and a Helm chart. Both are released independently.

### Releasing a new image version

To make a new release of the application image, run the following commands:

```sh
# set the version appropriately
export VERSION=vX.Y.Z
git checkout main
git pull
git tag -sam "Pipeline controller $VERSION" $VERSION
git push origin $VERSION
```

Pushing the tag will initiate a GitHub Actions workflow that builds and pushes the multi-platform container image [ghcr.io/weaveworks/pipeline-controller](https://github.com/weaveworks/pipeline-controller/pkgs/container/pipeline-controller).

Releasing a new image version will cause a GitHub Actions workflow to be kicked off that bumps the app version in the chart as well as the chart version itself and creates a PR from those changes. Merging that PR will kick off the chart release process as outlined below.

### Releasing a new chart version

The [pipeline-controller](./charts/pipeline-controller) chart is automatically released as an OCI artifact at [ghcr.io/weaveworks/charts/pipeline-controller](https://github.com/weaveworks/pipeline-controller/pkgs/container/charts%2Fpipeline-controller) whenever changes to it are merged into the `main` branch. CI checks are in place that verify any change to the chart is accompanied by a version bump to prevent overwriting existing chart versions.