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

To make a new release, run the following commands:

```sh
git checkout main
git tag -a -s v0.0.1 -m "Pipeline controller v0.0.1"
git push origin v0.0.1
```