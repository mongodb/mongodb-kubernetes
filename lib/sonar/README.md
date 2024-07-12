# Sonar 🐳

Work with multiple Docker images easily.

**Sonar is currently Work in Progress!**

## What is Sonar

Sonar is a tool that allows you to easily produce, template, build and publish
Dockerfiles and Docker images. It uses a declarative, multi-stage approach to
build Docker images.

## Quick start

Sonar can be used as a Python module or as a standalone program. Sonar will look
for an `inventory.yaml` file in your local directory that should contain a
collection of images to build and stages for each one of those images. A
different inventory file can be specified using `--inventory <file-path>`.

Sonar comes with an inventory file to be able to build itself, and to run its
unit tests. This [simple.yaml](inventories/simple.yaml) is:

``` yaml
vars:
  # start a local registry with:
  # docker run -d -p 5000:5000 --restart=always --name registry registry:2
  registry: localhost:5000

images:
- name: sonar-test-runner

  vars:
    context: .

  # First stage builds a Docker image. The resulting image will be
  # pushed to the registry in the `output` section.
  stages:
  - name: build-sonar-tester-image
    task_type: docker_build

    dockerfile: docker/Dockerfile

    output:
    - registry: $(inputs.params.registry)/sonar-tester-image
      tag: $(inputs.params.version_id)

  # Second stage pushes the previously built image into a new
  # registry.
  - name: tag-image
    task_type: tag_image

    source:
      registry: $(stages['build-sonar-tester-image'].output[0].registry)
      tag: $(stages['build-sonar-tester-image'].output[0].tag)

    destination:
    - registry: $(inputs.params.registry)/sonar-tester-image
      tag: latest
```

To execute this inventory file, you can do:

```
$ python sonar.py --image sonar-test-runner --inventory inventories/simple.yaml

[build-sonar-tester-image/docker_build] stage-started build-sonar-tester-image: 1/2
[build-sonar-tester-image/docker_build] docker-image-push: localhost:5000/sonar-tester-image:8945563b-248e-4c03-bb0a-6cc15cff1a6e
[tag-image/tag_image] stage-started tag-image: 2/2
[tag-image/tag_image] docker-image-push: localhost:5000/sonar-tester-image:latest
```

At the end of this phase, you'll have a Docker image tagged as
`localhost:5000/sonar-tester-image:latest` that you will be able to run with:

```
$ docker run localhost:5000/sonar-tester-image:latest
============================= test session starts ==============================
platform linux -- Python 3.9.4, pytest-6.2.4, py-1.10.0, pluggy-0.13.1
rootdir: /src
collected 38 items

test/test_build.py ...                                                   [  7%]
test/test_context.py ......x.....                                        [ 39%]
test/test_sign_image.py ..                                               [ 44%]
test/test_sonar.py ........                                              [ 65%]
test/test_tag_image.py .                                                 [ 68%]
test/test_tags.py ...........                                            [ 97%]
test/test_template.py .                                                  [100%]

======================== 37 passed, 1 xfailed in 0.52s =========================
```


## Legal

Sonar is released under the terms of the [Apache2 license](./LICENSE).
