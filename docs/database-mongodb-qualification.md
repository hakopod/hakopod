# MongoDB controller qualification

On September 30, 2026, five native development tests passed against the MongoDB controller image below. They covered standalone databases, replica sets, resizing, recovery and application bindings. The tests ran real MongoDB processes on two Linux amd64 Kubernetes nodes in the named Hakopod development cluster.

This record describes the tested artifact. It does not establish a production rollout, separate physical failure domains, or public-endpoint connectivity.

The [publication workflow](https://github.com/hakopod/hakopod/actions/runs/36789028666) published the accepted archive without rebuilding it. Anonymous downloads verified the version tag, manifest and all nine image blobs against their hashes. The installer and creation flow now use this qualified controller in the development source; the platform release remains pending.

## Source and image

The controller is built from [`mongodb/mongodb-kubernetes` at `75fa89bca8c1395a1beb0f244723f255f67f8719`](https://github.com/mongodb/mongodb-kubernetes/tree/75fa89bca8c1395a1beb0f244723f255f67f8719), the commit behind tag `1.13.0`. MongoDB Server is pinned to `8.0.32-ubi8`.

The controller image is:

```text
ghcr.io/hakopod/managed-mongodb-operator:1.13.0-hakopod.1@sha256:7ba576ba3115f007fcfc22c38b530ad05c211705a6a958124ec41ea80239a85c
```

The [Dockerfile](../Dockerfile.mongodb-operator) keeps the pinned upstream image as its base and replaces one file: the rebuilt operator binary. Verification checked every layer digest, the unchanged upstream layers and runtime configuration, the binary hash, and its `0755` file mode.

| Artifact | SHA-256 |
| --- | --- |
| Upstream base image | `0184a96e324f8fb6c53bedb8f859762166fe7e996d00214efe3ec51a502b3db3` |
| Rebuilt operator binary | `2dc206c3540413b5ab76ee2658e8d7037e5496fd769b7171f7bb8ab6c13110ad` |
| Applied source patch and regression test | `9e46c6273ea58369d055d73999f86b57722f10d852a8f2576a863b42cd2f4bb2` |
| OCI archive used for native acceptance | `e499c619bd401ad28dcc5885b1058349e6274d2796ba7a58a8e143a67546d392` |

## Why the controller has a patch

The upstream controller stalled when a TLS replica set grew from three members to five. It put the fourth member in AutomationConfig, then waited for that member's agent before requesting its pod. The three existing members stayed healthy, but scaling could not finish.

The correction requests the next pod before waiting for its agent during an existing TLS scale-up. Once that pod has been requested, the controller follows its existing configuration and readiness sequence. Bootstrap, shrinking, stable TLS deployments and plaintext ordering retain their existing paths.

The [patch script](../scripts/apply-managed-mongodb-patches.py) checks the exact upstream commit and refuses dirty source. The [regression test](../patches/mongodb-tls-scaling-test.go.txt) covers ten ordering cases. The unpatched baseline failed the three expected scale-up cases; the patched controller and readiness packages passed. The [build script](../scripts/build-managed-mongodb.sh) records the patch and binary hashes.

## Native acceptance results

Every case passed with exit code zero. No test failed or skipped. All five cases used identical engine source inventories and image pins; the inventories before and after each run matched. Each case had a fresh fixture. The existing development database was retained.

| Case | Runner seconds | What it checked |
| --- | ---: | --- |
| Standalone | 242.938 | Native TLS, certificate refusal, BSON data, application privileges, monitoring and certificate renewal |
| Cluster | 622.196 | The same checks across three members, including replica write refusal and renewal on every member |
| Scaling and quorum | 1154.829 | Three to five to seven members, then back to three; election, majority writes and data retention under bounded faults |
| Recovery | 363.096 | A separate recovery target, preserved BSON and collection metadata, and access fences |
| Application binding | 633.576 | Driver discovery, service-specific network access, trust refresh, binding revocation and credential-log checks |

TLS checks rejected plaintext, an untrusted issuer and a mismatched hostname. After certificate renewal, every member accepted the previous CA trust during the overlap period. The application account could read and write its database but could not inspect administrative replica-set status. These tests do not measure uninterrupted service under sustained traffic during certificate renewal.

The scaling test paused the former primary and verified election of another writable primary without losing majority-committed data. With both replicas paused, a reachable primary could not acknowledge a majority write. That failed acknowledgment has an ambiguous write outcome; the test does not claim the write was absent. Confirmed writes remained after recovery.

Recovery preserved raw BSON types, historical validation exceptions, unique and TTL indexes, views, capped collections and time-series collections. It checked that the source stayed unchanged, rejected a nonempty target, closed old native sessions and kept application access fenced until durable success and inspection.

The binding test used the MongoDB driver inside application containers. The bound service discovered all three members and read and wrote BSON data over TLS. An unbound service could not reach the members. After certificate renewal, the application received updated CA trust and read its existing data. Removing the binding removed the network grant. A bounded audit found none of the fixture's passwords or private keys in the inspected member, initialization, agent or controller logs.

All successful fixtures completed normal namespace and volume cleanup. Separate checks confirmed namespace absence and no remaining persistent-volume references. The test runners used bounded CPU, memory and time. A pressure watcher checked both development nodes and retained the test runners' evidence if it had to stop their children.

## Earlier failed attempts

The first scaling attempt exposed the upstream ordering defect and was stopped with its evidence preserved. The second attempt stopped before bootstrap completed because development scratch storage entered disk pressure. Kubernetes evicted the controller and the first member. That attempt did not qualify the patch.

Both failed fixtures were removed with ownership and UID checks; normal finalizers remained enabled. Namespace and volume reclamation were verified. The corrected run started after restoring the exact images, clearing the pressure condition and warming compilation separately. The runner now requires 16 GiB of free scratch storage before starting. Disk-pressure thresholds and node taints were not bypassed.

## Evidence and limits

The [native acceptance runner](../scripts/run-development-mongodb-acceptance.py) records source inventories, image pins, structured test events and hashes. The protected evidence files have these SHA-256 values:

| Evidence file | SHA-256 |
| --- | --- |
| `mongodb-native-scaling-v3.evidence.json` | `a48eec4d66cc2fba2e24a5e7a33b5a83769e7c5092a48914d2fcb5701c61989c` |
| `mongodb-native-cluster-v3.evidence.json` | `8cfcacf10c5f72bcb06ce8570450c0964df424df2e2b27de4ec557ba9c7cc22d` |
| `mongodb-native-recovery-v1.evidence.json` | `1d7cd5cf4b1e9bc6ad919ae1fd19e15bd7ee9c8dc51dd1190c64fd174a51f7e0` |
| `mongodb-native-standalone-v1.evidence.json` | `0f6d032250653e1161019c5454fbfc9a1489f3c11fe7f1f317f39b93a700be69` |
| `mongodb-native-binding-v1.evidence.json` | `16642b6a62bafd8e626373a153178bc31ca8f8c36eada728108cef5328cf86cd` |

The two development nodes share one VM. The bounded process pauses tested election and quorum behavior; they did not simulate a cloud outage, power loss or a physical zone failure. The suite did not qualify arm64, a sharded MongoDB deployment, high-load performance, an engine-version upgrade or a public Internet endpoint. Those results must be recorded separately before making those claims.
