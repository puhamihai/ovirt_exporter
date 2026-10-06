# ovirt_exporter
[![Go Report Card](https://goreportcard.com/badge/github.com/czerwonk/ovirt_exporter)](https://goreportcard.com/report/github.com/czerwonk/ovirt_exporter)

Exporter for oVirt engine metrics to use with https://prometheus.io/

This project is in maintenance mode now. No new features will be implemented by myself, merge requests are welcome.

## Install
```
go get -u github.com/czerwonk/ovirt_exporter
```

## Supported ressources
* hosts
* vms
* storagedomains
* snapshots (optional)

## Authentication
The exporter shares one API client (and one engine session) between all collectors.

* `-api.auth-method=oauth` (default): obtains an SSO access token once
  (`POST /ovirt-engine/sso/oauth/token`, `grant_type=password`, `scope=ovirt-app-api`)
  and sends it as `Authorization: Bearer` on every request. The token is renewed
  shortly before it expires, and revoked on shutdown.
* `-api.auth-method=session`: logs in once with HTTP Basic and `Prefer: persistent-auth`,
  then sends the `JSESSIONID` cookie on every request (the pre-0.13 behaviour).

When the engine invalidates the token/session, many requests receive a `401` at the same
time. Re-authentication is single-flight: exactly one login is sent, every other request
waits for its result and then retries once. After a failed login the client backs off
(5s, doubling up to 5m); requests fail immediately during the backoff instead of hitting
the engine. Credentials are never sent with HTTP Basic on ordinary API requests.

## Concurrency
`-api.max-concurrent-requests` (default `8`) caps the number of API requests in flight to
the engine at once. The cap is global: it is shared by all collectors and covers the
per-object fan-out (one request per VM for snapshots, statistics, NICs and disk
attachments, one per disk and per host NIC). Raise it only if scrapes take too long and
the engine has spare request threads; each request occupies one engine thread.

## Third Party Components
This software uses components of the following projects
* Prometheus Go client library (https://github.com/prometheus/client_golang)

## License
(c) Daniel Czerwonk, 2017. Licensed under [MIT](LICENSE) license.

## Prometheus
see https://prometheus.io/

## oVirt
see https://www.ovirt.org/
