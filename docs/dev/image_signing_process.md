**Table of Contents**

- [Docker Image Signing](#docker-image-signing)
    - [Summary](#summary)
    - [Docker Content Trust](#docker-content-trust)
        - [Root key](#root-key)
        - [Repo key](#repo-key)
        - [Signing key](#signing-key)
        - [Example](#example)
        - [Notary](#notary)
            - [Quay.io](#quayio)
            - [The future: Notary v2](#the-future-notary-v2)
    - [Our Release Process](#our-release-process)

# Docker Image Signing

## Summary
We will begin signing our images using Docker Content Trust.

The process will be totally integrated into our release process.

**NOTE: this is currently blocked as we wait for the security team**

## Docker Content Trust
Docker Content Trust is based on a set of keys:

- Root key
- Repo keys
- Signing keys

### Root key
Is the key used to initializing trust data on a repository.
That's the only moment in which it's used. As a result, the root key should be safely
stored offline and backed up regularly.

Loss of the root key is not recoverable.

### Repo key
The repository key is used to add/remove signers to a repository.
Loss of the repo key is recoverable.

### Signing key
The signing key is used to perform the actual signing of the image


### Example
- Create a signing key for the signer _nikolas_:
```
docker trust key generate nikolas --dir ~/.docker/trust
Generating key for nikolas...
Enter passphrase for new nikolas key with ID 554e51c:
Repeat passphrase for new nikolas key with ID 554e51c:
Successfully generated and loaded private key. Corresponding public key available: /Users/nikolas.de-giorgis/.docker/trust/nikolas.pub
```
- Add the signer to a quay.io repository (after enabling Local Trust in Repository Settings - Trust and Signing)
  - Since no root key is present, it will prompt to create one
  - Since this repo has no trust data initialized, it will also prompt to create the repo key
  ```
  docker trust signer add nikolas quay.io/nikolas_de_giorgis/signing_example --key ~/.docker/trust/nikolas.pub
  Adding signer "nikolas" to quay.io/nikolas_de_giorgis/signing_example...
  Initializing signed repository for quay.io/nikolas_de_giorgis/signing_example...
  You are about to create a new root signing key passphrase. This passphrase
  will be used to protect the most sensitive key in your signing system. Please
  choose a long, complex passphrase and be careful to keep the password and the
  key file itself secure and backed up. It is highly recommended that you use a
  password manager to generate the passphrase and keep it safe. There will be no
  way to recover this key. You can find the key in your config directory.
  Enter passphrase for new root key with ID 306e7c9:
  Repeat passphrase for new root key with ID 306e7c9:
  Enter passphrase for new repository key with ID 30972e0:
  Repeat passphrase for new repository key with ID 30972e0:
  Successfully initialized "quay.io/nikolas_de_giorgis/signing_example"
  Successfully added signer: nikolas to quay.io/nikolas_de_giorgis/signing_example
  ```

  At this point, signing can be done in two different ways:
  - `docker trust sign quay.io/nikolas_de_giorgis/signing_example:foo`
  - by setting the env var `DOCKER_CONTENT_TRUST` to `1` and regularly using `docker push`

  The second one is the best one as it makes integration into our release process and conditional
  signing much easier.

  ```
  export DOCKER_CONTENT_TRUST=1
  docker push quay.io/nikolas_de_giorgis/signing_example:foo
  The push refers to repository [quay.io/nikolas_de_giorgis/signing_example]
  ...
  foo: digest: sha256:b5cc72554894d6b76046ff6839df081eff75a10c127df7a9318d6cfb7549372f size: 2201
  Signing and pushing trust metadata
  Enter passphrase for nikolas key with ID 554e51c:
  Successfully signed quay.io/nikolas_de_giorgis/signing_example:foo
  ```

  Prompt for the passphrase can be avoided by setting the env var `DOCKER_CONTENT_TRUST_REPOSITORY_PASSPHRASE`


  We can verify that the image is signed in two different ways:
  - Either with
  ```
  docker trust inspect quay.io/nikolas_de_giorgis/signing_example
  [
    {
        "Name": "quay.io/nikolas_de_giorgis/signing_example",
        "SignedTags": [
            {
                "SignedTag": "foo",
                "Digest": "b5cc72554894d6b76046ff6839df081eff75a10c127df7a9318d6cfb7549372f",
                "Signers": [
                    "nikolas"
                ]
            }
        ],
        "Signers": [
            {
                "Name": "nikolas",
                "Keys": [
                    {
                        "ID": "554e51c5d1d596217a7b0d431612471294f40fe992ef93a16c44394090e29c60"
                    }
                ]
            }
        ],
        "AdministrativeKeys": [
            {
                "Name": "Root",
                "Keys": [
                    {
                        "ID": "18b1865e8711ea874a96c48ad5f99460855522d385aacc948c68389b6cf4f149"
                    }
                ]
            },
            {
                "Name": "Repository",
                "Keys": [
                    {
                        "ID": "30972e06872e82fb7a91a2413fecee2c8cf0ef9755bf4655beea38292f27938f"
                    }
                ]
            }
        ]
      }
   ]
  ```
  - or by the fact that having `$DOCKER_CONTENT_TRUST==1` makes it impossible to pull non-signed images:
  ```
   docker pull quay.io/nikolas_de_giorgis/signing_example:foo
   Pull (1 of 1): quay.io/nikolas_de_giorgis/signing_example:foo@sha256:b5cc72554894d6b76046ff6839df081eff75a10c127df7a9318d6cfb7549372f
   sha256:b5cc72554894d6b76046ff6839df081eff75a10c127df7a9318d6cfb7549372f: Pulling from nikolas_de_giorgis/signing_example
   Digest: sha256:b5cc72554894d6b76046ff6839df081eff75a10c127df7a9318d6cfb7549372f
   Status: Image is up to date for quay.io/nikolas_de_giorgis/signing_example@sha256:b5cc72554894d6b76046ff6839df081eff75a10c127df7a9318d6cfb7549372f
   Tagging quay.io/nikolas_de_giorgis/signing_example@sha256:b5cc72554894d6b76046ff6839df081eff75a10c127df7a9318d6cfb7549372f as quay.io/nikolas_de_giorgis/signing_example:foo
   quay.io/nikolas_de_giorgis/signing_example:foo
  ```
  while
  ```
  docker pull quay.io/nikolas_de_giorgis/signing_example:foo_not_signed
  No valid trust data for foo_not_signed
  ```



### Notary
Notary service is the base for the Docker Content Trust framework. [Reference](https://docs.docker.com/notary/service_architecture/)
#### Quay.io
Quay.io uses a different implementation of Notary, called [Apostille](https://github.com/coreos-inc/apostille).
This is currently not maintained and it has some bugs:
    - Theoretically, a repository with trust and signing enabled doesn't allow for the push of non signed tags, but that is currently possible. (Note that this has no effect on the client side, if they enforce `DOCKER_CONTENT_TRUST=1` they will not be able to pull the tag that is not signed)
    - The repository UI will show that every tag is NOT signed, even when they are.

These two bugs (and possibly more) will not be fixed, but RedHat is amongst the companies that are currently working on the design of Notary v2.

#### The future: Notary v2
The Notary project has been accepted into the CNFC; since then, a [massive collaboration](https://www.docker.com/blog/community-collaboration-on-notary-v2/) between various companies (Amazon, Microsoft, Docker, IBM, Google, Red Hat, Sylabs and JFrog) is in place to design and develop Notary V2.

There should be an update at KubeCon 2021 regarding it

Notary v2 should also integrate with tools such as AWS KMS


## Our Release Process
The signing process is integrated seamlessy into our release process through [this PR](https://github.com/10gen/ops-manager-kubernetes/pull/1365) (currently Draft as we wait for the security team). A new feature has been added to Sonar ([here](https://github.com/10gen/sonar/pull/15)) to integrate image signign.
To enable it, we just add to a `docker_build` task the following entries:

```yaml
  signer_name: <placeholder>
  passphrase_secret_name: <placeholder>
  key_secret_name: <placeholder>
  region: <placeholder>
```

Sonar will then fetch the key, stored in AWS Secret Manager, under the region `region` and name `key_secret_name`, and the corresponding `passphrase` (same region, name `passphrase_secret_name`), set `DOCKER_CONTENT_TRUST=1`, `DOCKER_CONTENT_TRUST_REPO_PASSPHRASE` to the value stored in the passphrase secret, and then perform a `docker push` operation with content trust enabled.

For extra safety, it will then wipe the key so nothing will stay stored on the host.
