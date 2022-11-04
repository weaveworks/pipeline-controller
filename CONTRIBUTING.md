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

This project only has unit tests for now. Those are run with

```sh
make test
```

Testing on this controller follows closely what is done by default in `kubebuilder` although we ditched the use of `Gomega` in favor of native Go testing structure. As an example on how to write tests for this controller you can take a look at Kubebuilder [docs](https://book.kubebuilder.io/cronjob-tutorial/writing-tests.html#writing-controller-tests) for reference.


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

Usually, when a new image version is published, a new chart version needs to be released as well. See below for instructions on that process.

### Releasing a new chart version

The [pipeline-controller](./charts/pipeline-controller) chart is automatically released as an OCI artifact at [ghcr.io/weaveworks/charts/pipeline-controller](https://github.com/weaveworks/pipeline-controller/pkgs/container/charts%2Fpipeline-controller) whenever changes to it are merged into the `main` branch. CI checks are in place that verify any change to the chart is accompanied by a version bump to prevent overwriting existing chart versions.
