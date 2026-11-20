# ovirt_exporter

Exporter for oVirt engine metrics to use with https://prometheus.io/

This project is in maintenance mode now. No new features will be implemented by myself, merge requests are welcome.

## Install

```
go get -u github.com/czerwonk/ovirt_exporter
```

## Supported ressources

- hosts
- vms
- storagedomains
- snapshots (optional)

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
per-object fan-out (one request per VM for snapshots, statistics and NICs, one per host
NIC). Raise it only if scrapes take too long and the engine has spare request threads;
each request occupies one engine thread.

## Disk metrics
`-with-disks` (default `true`) emits, per disk attachment,
`ovirt_vm_disk_provisioned_size_bytes` (virtual size), `ovirt_vm_disk_actual_size_bytes`
(space the active volume really occupies; on a block storage domain this is the size of
the LV behind a thin qcow2 disk) and `ovirt_vm_disk_total_size_bytes`, labelled with the
VM labels plus `disk_name`, `disk_alias`, `disk_logical_name`, `storage_domain`, `disk_id`,
`disk_format` (`cow`/`raw`) and `disk_sparse` (`true`/`false`).

It costs the same two requests per scrape whatever the fleet size: the VM listing is
fetched as `GET /vms?follow=disk_attachments` and joined in memory with one `GET /disks`.
Measured against two oVirt 4.5 engines (time to first byte, i.e. engine work):

| engine | VMs | attachments | `/vms` | `/vms?follow=disk_attachments` | `/disks` | nested `follow=disk_attachments.disk` |
|---|---:|---:|---:|---:|---:|---:|
| small | ~170 | ~270 | 1.5 s | 1.1 s | 0.9 s | 2.3 s |
| large | ~900 | ~1,300 | 2.7-3.9 s | 4.0 s | 1.1 s | **19.9 s** |

The nested `follow` is a single request but the engine assembles it per VM, single-threaded,
so it is not used. The previous implementation (one `GET /vms/{id}/diskattachments` per VM
and one `GET /disks/{id}` per attachment) needed ~2,200 requests per scrape on the large
engine. Exporter run end to end against the small engine with snapshots off: 0.13.1 fan-out
14.3 s per scrape, 0.14.0 1.5 s, which is also what a scrape without disk metrics takes; on
the large engine 4.97 s without disk metrics, 6.71 s with.

## Storage domain and VM state
`ovirt_storage_up` follows the storage domain's state **in its data center**: active `1`,
maintenance / preparing for maintenance `2`, anything else (inactive, locked, unknown,
activating, detaching, mixed) `0`. A domain attached to several data centers reports the
worst of them. An unattached domain has no data-center state and keeps the old encoding
(`external_status` `ok` is `1`), so a deliberately detached domain is not a permanent alert.

Previously the value came from `external_status`, which is the health an external provider
plug-in would report and stays `ok` on an engine without one. An engine that deactivated a
data domain because no host could see its LUNs, and paused the VMs on it, still reported
`ovirt_storage_up` `1` for that domain. The system-scope
`GET /storagedomains` carries no `status` element for an attached domain; the state only
exists in `GET /datacenters/{id}/storagedomains`, so the collector adds one request for the
data center list plus one per data center (fetched in parallel) to every scrape.

Two info metrics expose the engine's raw state strings, value always `1`:

* `ovirt_storage_status{name,type,path,datacenter,status}`, one series per data center the
  domain is attached to (`datacenter=""` and `status="unattached"` for an unattached one).
* `ovirt_vm_status{name,host,cluster,status}` (`up`, `down`, `paused`, `suspended`,
  `not_responding`, `migrating`, `unknown`, ...). `ovirt_vm_up` is `0` for both a VM the
  engine paused on a storage error and one that is simply powered off; a rule for "VM paused
  on storage error" is `ovirt_vm_status{status="paused"} == 1`.

They mirror `ovirt_host_status`. If a data center's listing fails during a scrape, the
domains attached to it get no `ovirt_storage_up` series for that scrape (their state is
unknown, and the old value would have said "up") while their capacity series and the states
that were fetched are still emitted.

## Third Party Components

This software uses components of the following projects

- Prometheus Go client library (https://github.com/prometheus/client_golang)

## License

(c) Daniel Czerwonk, 2017. Licensed under [MIT](LICENSE) license.

## Prometheus

see https://prometheus.io/

## oVirt

see https://www.ovirt.org/
