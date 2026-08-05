# Unikraft machine image

This example packages the Omnara machine daemon in a Unikraft Cloud image using
the multiprocess-capable `base-compat` runtime.

From the repository root, build and publish the image to a namespace accessible
by the Unikraft token configured in Omnara:

```sh
./examples/unikraft-machine/build.sh <organization>/omnara-machine:v1 0.1.0
```

Create an Omnara machine pool with the published image and grant the pool to a
project. Omnara uses `/usr/local/bin/omnarad` to seed its managed installation,
so the image does not need a default `cmd`.

The example uses `base-compat:latest` to match the currently published Unikraft
runtime. Pin a tested runtime version or digest before using the image in a
production environment.
