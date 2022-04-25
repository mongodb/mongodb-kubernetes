# Connecting to an HTTPS Ops Manager

**note: SSL protocols are completely deprecated in favor of TLS as of
many years now; unfortunately, the industry keeps refering to TLS as
SSL more often than not. In the internal documentation, aimed at
developers of the Operator, we use TLS (preferably) and also SSL
(discouraged). In any communication to customers we should use both.**

Customers might have an instance of Ops Manager running over HTTPS, in
which case the TLS certificate used might be not recognized by the
public trust chain included in the operator and database images. If
this was the case the operator, automation agents nor curl would be
able to connect "safely" or validate the certificate presented by Ops
Manager. This is the case of customers having their own certificate
authorities, and signing their own certificates.

If Ops Manager's TLS certificate is unknown 3 different pieces of
software will fail:

* Operator: will fail at using the Ops Manager API.
* curl: won't be able to download the automation agent for the
  database image.
* Automation Agent: will also fail at using the Ops Manager API, and
  any other Ops Manager related tasks (like downloading versions from
  Local mode)

When setting the TLS properties of the connection we have to configure
all of them, and each one require a different set of configuration
options.

## Activating TLS Options into the 3 systems

We will use the *Automation Agent* TLS options to connect to Ops
Manager and map these options to the different requirements for each
one of them. The [configuration
options](https://docs.opsmanager.mongodb.com/current/tutorial/configure-ssl-connection-to-web-interface/)
related to TLS, for the Operator:

* `sslRequireValidMMSServerCertificates`: If true will require the SSL
  certificate is valid. If `false` will work with *invalid*
  certificates (like self signed certs).

* `sslTrustedMMSServerCertificate`: Location in disk of a CA
  certificate that will validate the server certificate presented by
  Ops Manager.

## Where to Define this Configuration

The Ops Manager TLS configuration will go in the *Project* object,
along with `projectName` and `baseUrl`.

* TODO: What names will these attributes have?

## Operator

The operator is a piece of `go` code that uses the standar HTTP `go`
libraries. In the `http.go` file we implement the
`NewInsecureHTTPClient` and `NewCAValidatedHTTPClient`; they
correspond to the two configuration options described.

If the user passed `sslRequireValidMMSServerCertificates` equals to
`false` then the Operator will use an *insecure client* to connect.

If the user passed the `sslTrustedMMSServerCertificate` this will
correspond to a `ConfigMap` containing a `mms-ca.crt` entry that will be
loaded into the trust chain at runtime. Every *Project* might have its
own CA certificate, and we won't know until the MDB object has been
applied for the first time or created, therefore, the certificate
can't be mounted as a volume, but its contents will be passed to the
operator instead.

## Database Image

The database image will try to download the Automation Agent from Ops
Manager, and it has to respect the *SSL* configuration applied so far.

If the user passes `sslTrustedMMSServerCertificate` this value should
point to a `ConfigMap` with a `mms-ca.crt` entry that will be mounted in
`/mongodb-automation/certs/mms-ca.crt`. There's just one Ops Manager
`mms-ca.crt` file per database image, and it is know before hand, so it
will be mounted.

## Downloading the Automation Agent with curl

If `sslTrustedMMSServerCertificate` is passed, `curl` will be run with
the option `--cacert` pointing at `/mongodb-automation/certs/ca.crt`/

If the user passes `sslRequireValidMMSServerCertificates` with a value
of false, then the database image will run `curl` with the
`--insecure` option.

## Running the Automation Agent

Both `sslTrustedMMSServerCertificate` and
`sslRequireValidMMSServerCertificates` corrrespond to argument options
of the automation agent and they will be passed as they are.
