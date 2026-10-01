# What a database cluster topology should tell you

Draft article. Validate all product availability statements against the release
support matrix before publication.

A database cluster is more than several containers. It has members with different
roles, a replication relationship, storage that must survive replacement and
rules for choosing a primary. A useful topology makes those relationships visible
and tells you when the underlying observations are stale.

Start with the distinction between desired and observed state. Asking for a
primary and two replicas describes the target. It does not prove that three
members exist, that replication is healthy, or that they run on three machines.
The control plane should show both counts, along with the time and revision of
its last observation.

Next, inspect placement. Three members on one worker can all disappear when
that worker fails. Three workers in one zone still share a zone failure domain.
Even different provider names do not prove independent storage, networking or
control-plane availability. Placement is evidence you can inspect, not a
substitute for understanding the failure model.

Connection routing also differs by engine. PostgreSQL applications can choose a
primary endpoint for writes and a replica endpoint for eligible reads. PgBouncer
reduces connection overhead; it does not automatically decide which arbitrary
SQL statements are safe to send to replicas. Replica reads may lag committed
writes, so read-after-write requirements need an explicit application policy.

Redis Cluster clients discover slot ownership and follow MOVED and ASK
responses. A normal TCP load balancer does not remove that requirement. MongoDB
drivers discover replica-set members and apply read preferences. ClickHouse
shards and replicated tables have different responsibilities again. A topology
should explain the engine's actual routing model.

Applications belong in the same picture. A checkout API might write through the
primary endpoint while a reporting worker reads through the replica endpoint.
Drawing those bindings makes the impact of a database change easier to inspect.
The diagram should group by application, reveal the individual service and
variable on selection, and keep the endpoint between the application and the
database member.

Keep deployment evidence separate from live sessions. A saved binding describes
the next requested configuration. The previous successful deployment can still
matter when a replacement fails. Removing a binding from the newest application
configuration does not prove that all older pods stopped using it. Show those
revisions and keep missing endpoints explicit.

Test the topology with realistic density. Fifteen applications and six replicas
reveal overlapping targets, clipped names and navigation problems that a
two-node illustration hides. Use readable node sizes, a bounded scrolling
canvas, search, application pages and a keyboard-accessible node inspector.
Never invent traffic animation or healthy status to fill the diagram.

Finally, distinguish monitoring from illustration. CPU and memory samples are
measured usage. A configured memory limit is capacity. A replication line shows
a relationship unless actual throughput or lag is measured and labeled. An
unknown metric should stay unknown.

Use the [managed database guide](../managed-databases.md) for the current
PostgreSQL and Redis configuration, connection and recovery workflows. The
[platform expansion plan](../managed-database-platform-plan.md) clearly separates
planned engines and endpoint work from implemented behavior.
