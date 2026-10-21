---
description: >
  Horizontal Scaling of kcp through sharding
---

# Shards

To learn how to run a sharded environment for development, see [Running a Sharded Environment](../../developers/running-sharded.md).

Every kcp shard is hosting a set of logical clusters. A logical cluster is
identified by a globally unique identifier called a **cluster name**. A shard
serves the logical clusters under `/clusters/<cluster-name>`.

A set of known shards comprises a kcp installation.

## Root Logical Cluster

Every kcp installation has one logical cluster called `root`. The `root` logical
cluster holds administrational objects. Among them are shard objects.

## Shard Objects

The set of shards in a kcp installation is defined by `Shard` objects in
`core.kcp.io/v1alpha1`.

A shard object specifies the network addresses, one for external access (usually
some worldwide load balancer) and one for direct access (shard to shard).

## Logical Clusters and Workspace Paths

Logical clusters are defined through the existence of a `LogicalCluster` object
"in themselves", similar to a `.` directory defining the existence of a directory
in Unix.

Every logical cluster name `name` is a **logical cluster path**. Every logical
cluster is reachable through `/cluster/<path>` for every of their paths.

A `Workspace` in `tenancy.kcp.io/v1alpha1` of name `name` references a logical
cluster specified in `Workspace.spec.cluster`. If that workspace object resided
in a logical cluster reachable via a path `path`, the referenced logical cluster
can be reached via a path `path:name`.

## Canonical Paths

The longest path a logical cluster is reachable under is called the **canonical
path**. By default, all canonical paths start with `root`, i.e. they start in
root logical cluster.

The logical cluster object annotated with `kcp.io/path: <canonical-path>`.

Additional subtrees of the workspace path hierarchy can be defined by creating
logical clusters with `kcp.io/path` annotation _not_ starting in `root`. E.g.
a home workspace hierarchy could start at `home:<user-name>`. There is no need
for the parent (`home` in this case) to exist.

## Front Proxy

A front-proxy is aware of all logical clusters, their shard they live on,
their canonical paths and all `Workspaces`. Non canonical paths can be
reconstructed from the canonical path prefixes and the workspace names.

Requests to `/cluster/<path>` are forwarded to the shard via inverse proxying.

A front-proxy in its most simplistic (toy) implementation watches all shards.
Clearly, that's neither feasible for scale nor good for availability. A front-
proxy could alternative be backed by any kind of external database with the
right scalability and availability properties.

There can be one front-proxy in front of a kcp installation, or many, e.g. one
or multiple per region or cloud provider.

## The Admin Workspace (`:admin`)

Every shard serves the *Admin workspace* at `/services/admin`: an
installation-wide administrative view, reachable identically through any
shard. Its resources are aggregated views of shard-owned objects: all
`Shard` objects, and the `APIIdentity` registry (see
[Core API identities](#core-api-identities)). The views are backed by the
cache server, where every shard's objects are replicated, so all shards
serve an identical view in a single `resourceVersion` space: consumers can
fail over between shards and resume watches with the same resource version.
Over time the Admin workspace is the home for further installation-wide
admin surfaces.

The view is writable for exactly one purpose: the allow-listed operational
fields, currently the `experimental.core.kcp.io/unschedulable` annotation
for cordoning and `spec.resourceLimits` for scheduling limits. Such a write
is validated, applied to the cache copy of the target shard, and flows from
the cache to the shard hosting the authoritative object, which applies it -
no component ever connects to another shard directly, and intent for a
temporarily unavailable shard parks in the cache until it reconnects.
Everything else on a `Shard` is read-only through the Admin workspace:
shards register themselves and own the rest of their configuration.

Because such a write only records intent, the owning shard acknowledges it
in status, which travels the other way (shard -> cache) and is therefore
proof that the round trip completed: the `Schedulable` condition for
cordoning, and the `ResourceLimitsApplied` condition - whose message names
the limits in force - for the scheduling limits.

The kcp workspace plugin exposes it as the reserved pseudo-workspace
`:admin`:

```sh
$ kubectl ws use :admin
Current workspace is ':admin' (aggregated view of all shards).
$ kubectl get shards
NAME      REGION      URL                      ...
root      us-east-2   https://127.0.0.1:6444   ...
shard-1   us-east-1   https://127.0.0.1:6445   ...
$ kubectl get apiidentities
NAME                       OWNER                  CORE   AGE
system-tenancy.kcp.io      root|tenancy.kcp.io    true   3d
2v9w7bq1rt2mdzn6-5f1c...   2v9w7bq1rt2mdzn6|net   false  2d
```

Access requires membership in the `system:kcp:admin` group; the serving
path does not depend on the root shard. To reach the view through a
front-proxy, add a `/services/` path mapping; any shard can be the backend.

The front-proxy itself discovers shards through the Admin workspace: the
kubeconfigs given via `--shard-peer-kubeconfig` (falling back to the
`--root-kubeconfig` server) are seeds, used to bootstrap discovery and as a
last-resort fallback only. Every shard observed through the view joins the
failover peer set at runtime and decommissioned shards leave it; requests are
distributed across the discovered shards, not the seeds. With short-lived
shards the discovery channel therefore keeps working as long as any currently
registered shard is reachable, even after all seeds are gone, and stale seeds
do not receive traffic.

The `root` workspace keeps a familiar read-only view: the shard hosting the
root workspace runs a controller that mirrors the cache server's Shard
objects into `root` as representations, so `kubectl get shards` in `root`
continues to work. The representations are eventually consistent and
informational; the Admin workspace remains the authoritative surface and the
only place operational writes (cordoning, scheduling limits) are accepted.

`Shard` objects in the `root` workspace are protected by admission: shards
register themselves and own their objects, so all direct writes (create,
update including annotations, delete) by non-system users are denied with a
pointer at the Admin workspace. Shard configuration changes go through the
shard's own flags/deployment; operational actions - cordoning via the
`experimental.core.kcp.io/unschedulable` annotation and scheduling limits via
`spec.resourceLimits` - go through the Admin workspace.

## Consistency Domain

Every logical cluster provides a Kubernetes-compatible API root endpoint under
`/cluster/<path>` including its own discovery endpoint and their own set of
API groups and resources.

Resources under such an endpoints satisfy the same consistency properties as with
a full Kubernetes cluster, e.g. the semantics of the resource versions of one
resource matches that of Kubernetes.

Across logical clusters the resource versions must be considered as unrelated,
i.e. resource versions cannot be compared.

## Wildcard Requests

The only exception to the upper rule are objects under a "wildcard endpoint"
`/clusters/*/apis/<group>/<v>/[namespaces/<ns>]/resource:<identity-hash>` per
shard. It serves the objects of the given resource on that shard across
logical-clusters. The annotation `kcp.io/cluster` tells the consumer which
logical cluster each object belongs to.

The wildcard endpoint is privileged (requires `system:masters` group membership).
It is only accessible when talking directly to a shard, not through a
front-proxy.

Note: for unprivileged access, virtual view apiservers can offer a highly
secured and filtered view, usually also per shard, e.g. for owners of APIs.

## Cross Logical Cluster References

Some objects reference other logical clusters or objects in other logical
clusters. These references can be by logical cluster name, by arbitrary logical
cluster path or by canonical path.

Referenced logical clusters must be assumed to live on other shard, maybe even
on shard of different regions or cloud providers.

For scalability reasons, it is usually not adequate to drive controllers by
informers that span all shard.

In other words, logical involving cross-logical-cluster referenced have a cost
higher than in-logical-cluster references and logic must be carefully planned:

For example, during workspace creation and scheduling the scheduler running on
the shard hosting the `Workspace` object will access another shard to create the
`LogicalCluster` object initially. It picks a target shard at random from the
set of valid `Shard` objects matching `Workspace.spec.location.selector` (see
[`chooseShardAndMarkCondition`][choose-shard]), then generates an
optimistic logical-cluster name (a 16-character base36 hash of a random 32-byte
token — see [`randomClusterName`][random-name], which gives
P(collision) < 10⁻⁹ for 2²⁶ clusters) and tries to create the `LogicalCluster`
on that shard. On `AlreadyExists` it checks whether the existing object belongs
to this `Workspace`; if not, it drops the cluster-name annotation and the next
reconcile loop picks a new name and shard. The `Workspace`-hosting shard does
not maintain a continuous watch on the remote `LogicalCluster`; it relies on
the periodic reconcile of the workspace to observe initialization progress.

[choose-shard]: https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/tenancy/workspace/workspace_reconcile_scheduling.go#L240
[random-name]: https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/tenancy/workspace/workspace_reconcile_scheduling.go#L427

Another example is API binding, but it is different than workspace scheduling:
a binding controller running on the shard hosting the `APIBinding` object will
be aware of all `APIExport`s in the kcp installation through caching replication
(see next section). What is special is that this controller has all the
information necessary to bind a new API and to keep bound APIs working even if
the shard of the `APIExport` is unavailable.

Note: usually it a bad idea to create logic dependent on the parent workspace. If
such logic is desired, for availability and scalability reasons some kind of
replication is required. E.g. a child workspace must stay operation even if the
parent is not accessible.

## Cache Server Replication

The cache server is a special API server that can hold replicas of objects that
must be available globally in an eventual consistent way. E.g. the `APIExport`s
and `APIResourceSchemas` are replicated that way and made available to the
corresponding controllers via informers.

The cache server holds objects by logical clusters, and it can hold objects from
many or all shards in a kcp installation, served through wildcard informers.
The resource versions of those objects have no meaning beyond driving the cache
informers running in the shards.

Cache servers can be 1:1 with shards, or there can be shared cache servers, e.g.
by region.

Cache servers can form a cache hierarchy.

Controllers that make use of cached objects, will usually have informers against
local objects and against the same objects in the cache server. If the former
returns a "NotFound" error, the controllers will look up in the cache informers.

The cache server technique is only useful for APIs whose object cardinality
across all shards does not go beyond the cardinality sensibly storable in a
kube-based apiserver.

Note that objects like `Workspace`s and `LogicalCluster`s fall not into that
category. This means that in particular the logical cluster canonical path
cannot be derived from cached `LogicalCluster`s. Instead, the cached objects
must hold their own `kcp.io/path` annotation in order to be indexable by that
value. This is crucial to implement cross-logical-cluster references by
canonical path.

Note: the `APIExport` example assumes that there are never more than e.g. 10,000
API exports in a kcp installation. If that is not an acceptable constraint,
other partitioning mechanism would be need to hold the number of `APIExport`
objects per cache server below the critical number. E.g. there could be cache
servers per big tenant, and that would hold only public exports and
tenant-internal exports. A more complex caching hierarchy would make sure the
right objects are replicated, while the "really public" exports would only be a
small number.

## Replication

Each shard pushes a restricted set of objects to the cache server. As the cache
server replication is costly, this set is as minimal as possible. For example,
certain RBAC objects are replicated in case they are needed to successfully
authorize bindings of an API, or to use a workspace type.

By the nature of replication, objects in the cache server can be old and
incomplete. For instance, the non-existence of an object in the cache server
does not mean it does not exist in its respective shard. The replication
could be just delayed or the object was not identified to be worth to replicate.

## Controllers and Sharding

### Where controllers run

Standard kcp controllers (workspace scheduler, APIBinding, APIExport endpoint
slices, replication, RBAC labelers, etc.) run on **every shard**. There is no
leader election across shards; each shard owns the logical clusters scheduled
to it and reconciles its own subset of objects through wildcard informers
scoped to that shard. This works because objects of a given logical cluster
live on exactly one shard.

### Dual-informer pattern: local + cache fallback

Most cross-shard-aware controllers run two informers per type:

- a **local** informer over the shard's own etcd, and
- a **global** informer over the cache server (via the
  `CacheKcpSharedInformerFactory`).

Lookups follow a "local first, cache fallback" pattern, typically via
[`indexers.ByPathAndNameWithFallback`][by-path-fallback]: try the local copy,
then the cached copy if the object lives on a different shard. The APIBinding
controller is a representative example — it resolves an `APIExport` reference
by trying the local informer and falling back to the cache.

[by-path-fallback]: https://github.com/kcp-dev/kcp/blob/main/pkg/indexers/indexers.go

### Cross-shard write pattern

!!! warning "Anti-pattern, tracked for removal"
    Today the workspace scheduler writes directly to a remote shard to create
    the `LogicalCluster`. This is the only common cross-shard write path in
    kcp and we want to replace it with a pull-based, cache-mediated handoff.
    Tracked in [kcp-dev/kcp#4054](https://github.com/kcp-dev/kcp/issues/4054).
    New controllers should not introduce additional cross-shard writes.

When a controller has to write to another shard (workspace scheduling is the
only common case today), it acquires an admin client targeted at that shard's
`spec.baseURL` (see [`createLogicalCluster`][create-lc]). There is no special
backoff or circuit breaker for cross-shard writes — failures bubble up as
queue requeues with the standard rate-limited backoff. This means cross-shard
writes are best kept rare and idempotent.

[create-lc]: https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/tenancy/workspace/workspace_reconcile_scheduling.go#L290

### Front-proxy routing

The front-proxy maintains an in-memory index built by the
[workspace-index controller][index-controller]: it watches `Shard` objects on
the root shard, then for each shard starts informers over its `Workspace` and
`LogicalCluster` objects. Requests to `/clusters/<path>/...` are resolved by
exact-match lookup against this index and reverse-proxied to the owning
shard. Unknown paths return 403. The proxy itself does not perform consistent
hashing or load balancing across shards; routing is purely a function of where
a logical cluster was scheduled.

[index-controller]: https://github.com/kcp-dev/kcp/blob/main/pkg/proxy/index/index_controller.go

## Failure Modes and Bottlenecks

### Shard unavailability

There is no automatic failover for an unhealthy shard. The
[`isValidShard`][is-valid] check is currently a no-op and shard health is
inferred only from request errors at the moment of access. Concretely:

- **Workspaces already scheduled** to the unavailable shard are unreachable
  through the front-proxy until it returns.
- **Workspace scheduling** continues to succeed for new workspaces as long as
  at least one valid shard is reachable; the random selector simply skips
  shards annotated `kcp.io/unschedulable`.
- **Bound APIs** on a workspace whose `APIExport` lives on an unavailable
  shard keep functioning: the CRD is materialized locally and the binding
  controller already has the schema cached. New bindings or schema updates
  will stall until the export's shard returns.
- **Cross-shard writes** (e.g. workspace creation racing against the target
  shard's outage) requeue and retry with the standard workqueue backoff.

[is-valid]: https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/tenancy/workspace/workspace_reconcile_scheduling.go#L423

### Logical cluster migration

Workspaces can be moved between shards using the logical cluster migration feature. This allows
rebalancing workspaces across shards or evacuating a shard for maintenance. During migration,
the workspace is temporarily unavailable. See [Logical Cluster Migration](migration.md) for
details.

### Cache server bottlenecks

The cache server is the only fan-in point for cross-shard data, which makes it
the most important scaling consideration in a kcp installation:

- **Cardinality is bounded by what a single apiserver can hold.** The cache
  server is an `apiextensions-apiserver` backed by etcd. The total count of
  replicated objects across all shards must fit in one such apiserver. As a
  rule of thumb, treat ~10,000 objects of any single replicated type as the
  threshold to start planning partitioning (see "Partitioning strategies"
  below).
- **Write amplification is N → 1.** Every shard pushes its replication-marked
  objects to the same cache server. Large numbers of frequently-updated
  replicated objects (e.g. webhook configurations or RBAC marked for
  replication) translate directly into write load on the cache.
- **Replication is eventually consistent.** Controllers reading from the
  cache informer can observe stale or temporarily missing objects.
  Reconciliation logic must tolerate this — never use the cache as the
  authoritative source for "does X exist?" decisions, and never compare cache
  resource versions to local resource versions.
- **Cache server unavailability degrades, not breaks.** Local informers and
  the local apiserver keep working; what stops is cross-shard visibility.
  New `APIBinding`s referencing exports on other shards will fail to resolve
  until the cache returns.

### Front-proxy bottleneck

The default front-proxy implementation watches `Shard`, `Workspace`, and
`LogicalCluster` objects across every shard. This is fine for tens of shards
but does not scale to large installations. Production deployments are
expected to back the proxy index with an external store rather than rely on
the toy in-memory implementation, or to run multiple regional proxies each
serving a subset of shards.

### Partitioning strategies

The only partitioning lever implemented today is **`Partition` and
`PartitionSet` objects**, which scope `APIExportEndpointSlice` consumption to
a subset of shards (see [Partitions](partitions.md)). This reduces per-
controller informer load by letting an `APIExport`'s consumers talk only to
the shards in their partition, but it does not reduce cache-server
cardinality — every replicated object from every shard still lives in the
single cache server.

!!! warning "Future work, not implemented"
    Two further scaling levers are commonly discussed but **not built today**:

    - **Per-region (or per-tenant) cache servers** — currently the cache
      endpoint is a single global config (`--cache-kubeconfig`); there is no
      mechanism to map shards to different caches.
    - **Cache hierarchies** — the replication path is strictly
      shard → cache; there is no cache → cache forwarding, so a regional
      cache cannot promote a curated subset to a higher-level global cache.

    Both are tracked in
    [kcp-dev/kcp#4055](https://github.com/kcp-dev/kcp/issues/4055). Until
    they exist, the cache server is an installation-wide scaling ceiling and
    a single point of failure for cross-shard visibility.

## Bootstrapping

A new shard starting up will run a number of standard controllers (e.g. for
workspaces, API bindings and more). These will need a number of standard
informers both watching objects locally on that shard through wildcard informers
and watching the corresponding cache server.

Wildcard informers require the `APIExport` identity. The core APIs
(`tenancy.kcp.io`, `topology.kcp.io`, `shards.core.kcp.io`,
`migration.kcp.io`, `cache.kcp.io`) have their `APIExport` in the root
logical cluster, so every shard, the front-proxy and the virtual-workspaces
server need these identities before they can do any work. How they get them
is described next; after retrieving them the informers can be started.

## Core API identities

An identity is part of every cross-workspace request for a core API, e.g.
`/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces:<identity>`.
Historically the root shard created the identities randomly on its first
boot and every other component asked the root shard for them, so nothing
could start while the root shard was down.

With the `APIIdentityDecoupling` feature gate enabled, a fresh installation
gives its core APIExports well-known identities (`system-tenancy.kcp.io`,
`system-topology.kcp.io`, ...), so there is nothing to look up. Existing
installations keep the random identities they have; nothing changes for them.
Either way every identity is listed in the `APIIdentity` registry described
below, which the cache server replicates to every shard and the Admin
workspace serves, so shards, virtual-workspaces servers and the front-proxy
find the core identities without the root shard.

Where each component gets its identities, in order of preference:

| Component | Sources |
|---|---|
| root shard | its own APIExports in the `root` workspace |
| other shards | the deprecated `apiexport-identity-cache` ConfigMap (what the shard already uses) → the `APIIdentity` registry on the cache server → the root workspace's APIExports replicated to the cache server → the root shard via `--root-shard-kubeconfig-file` (legacy) |
| virtual-workspaces | its shard's ConfigMap → the registry → the replicated root APIExports → the root workspace |
| front-proxy | the `APIIdentity` registry in `:admin` of its peers → the root shard via `--root-kubeconfig` (legacy) |

- An existing shard restarts without the root shard or the cache server as
  long as its ConfigMap copy is there; the ConfigMap is deprecated and goes
  away with `secretRef`.
- A new shard gets the identities from the cache server.
- The front-proxy starts and reports itself live right away. It only becomes
  ready once it has the identities (readiness check `core-identities`).

### `APIIdentity`

Every APIExport identity in the installation, core or not, is listed as an
`APIIdentity` named after the identity value. The shard hosting the owning
export derives it into its `system:shard` logical cluster; all shards' objects
are replicated to the cache server and shown together in the Admin workspace:

```sh
$ kubectl ws use :admin
$ kubectl get apiidentities
NAME                          OWNER              CLUSTER   CORE    SHARED   LEGACY   AGE
system-tenancy.kcp.io         tenancy.kcp.io     root      true                      3d
2v9w7bq1rt2mdzn6-5f1c...      networking         2v9w...   false   True              2d
```

```yaml
apiVersion: admin.kcp.io/v1alpha1
kind: APIIdentity
metadata:
  name: 2v9w7bq1rt2mdzn6-5f1c...
spec: {}                            # operator knobs of the legacy conversion only
status:
  owner: {cluster: 2v9w7bq1rt2mdzn6, export: networking}
  groupResources:
  - {group: networking.io, resource: vnets}
  storageSharedWith:                # exports whose bindings sit on this prefix
  - export: {cluster: 1k3f..., export: compute}
    resources: [{group: networking.io, resource: vnets}]
  conditions:
  - type: StorageShared
    status: "True"
```

The object outlives its export: when the owner is deleted it stays with
`Orphaned: "True"` until no binding on the shard stores under the identity
any more, so that no other export can pin the identity while consumers' data
may still sit under it.

Identities that several exports hold, which was possible by copying an
identity secret, are reported with `LegacyShared: "True"` and
`status.legacyHolders`. Nothing changes for them until an administrator asks
for a conversion with `spec.convert: true` (the `APIIdentityDecoupling` feature
gate must be enabled on the shards hosting the holders): the owner
(`spec.owner`, by default the oldest holder) keeps the identity, every other
holder gets a fresh identity and keeps serving its consumers' objects from the
prefix of the formerly shared identity, so no data moves. Permission claims
that named the shared identity for a converted holder's resources stop matching
it and have to be updated by their owners. A converted export can later move
its consumers' objects onto its own prefix with a
[storage re-keying](../apis/storage-rekeying.md). `spec.convert: false` holds a
conversion explicitly. Only `spec` may be written through the Admin workspace;
everything else is derived.

