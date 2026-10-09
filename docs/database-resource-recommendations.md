# Database resource recommendations

Hakopod reports an advisory per-member size before database creation and resize. It does not apply the recommendation, reserve capacity, or change admission rules. The create and resize reviews continue to use the resource values that the user submits.

Policy version 1 starts from an engine baseline:

| Engine | CPU per member | Memory per member |
| --- | ---: | ---: |
| PostgreSQL, Redis, DuckDB | 250m | 512 MiB |
| MySQL, MongoDB, Vitess | 500m | 1 GiB |
| ClickHouse | 500m | 2 GiB |
| Oracle | 1 CPU | 4 GiB |

Three connected services raise the baseline to at least 500m CPU and 1 GiB. Nine connected services raise it to at least 1 CPU and 2 GiB. Counts come from current saved application specifications in the database scope and stop at a documented bound.

When bounded current or 24-hour samples exist, the policy uses the larger per-member observation. It adds 25 percent CPU headroom and 50 percent memory headroom, then rounds up to a supported step. Aggregate history is divided by the desired member count, so the 24-hour value is an estimate. A recommendation with no samples has low confidence. Current evidence plus at least 60 usable 24-hour samples has high confidence. Other sampled recommendations have medium confidence.

The capacity plan also reports the exact recovery allocation used by database admission. This total includes replacement members, supporting processes, recovery helpers, managed platform peak allocation, and application allocation when the scope shares a managed capacity pool. A resize excludes the current database from used capacity and retains any larger existing recovery reservation. When the installation does not provide a complete capacity policy, the API reports capacity as unknown instead of zero.

Recommendations are workload estimates. They do not guarantee latency, throughput, availability, or that a future admission request will succeed.
