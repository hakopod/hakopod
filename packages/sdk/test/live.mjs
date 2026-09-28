import assert from "node:assert/strict";
import { Hakopod, service, APIError } from "../dist/index.js";

if (process.env.HAKOPOD_SDK_LIVE_TEST !== "1")
  throw new Error("Use the Go TestSDKLiveLifecycle development fixture");
const hako = await Hakopod.connect();
const suffix = crypto.randomUUID().slice(0, 8);
const appName = `sdk-fixture-${suffix}`;
const networkName = `sdk-network-${suffix}`;
let database,
  application,
  networkCreated = false;
const failures = [];
async function cleanup(work) {
  try {
    await work();
  } catch (error) {
    failures.push(error);
  }
}
const network = hako.network(networkName);
try {
  const net = await network.plan({
    segments: { backend: { applications: [appName] } },
  });
  await net.apply();
  networkCreated = true;
  assert.equal((await network.get()).spec.name, networkName);
  const creation = await hako
    .db(`sdk-db-${suffix}`)
    .create({
      engine: "postgresql",
      cpu: "100m",
      memory: "256Mi",
      storageGiB: 1,
    });
  database = hako.database(creation.databaseId);
  await creation.wait({ timeoutMs: 6 * 60_000 });
  assert.equal((await database.get()).status, "ready");
  const binding = await database.binding();
  const review = await hako.app(appName).plan({
    services: {
      web: service({
        image:
          "nginxinc/nginx-unprivileged:stable-alpine@sha256:adf5042a17f4ecdd200c595fa9ffd1be37efb18f89a830bd1a00e4ab4d59d42c",
        port: 8080,
        public: false,
        bindings: { DATABASE_URL: binding },
        networks: ["backend"],
      }),
      checker: service({
        image: "ghcr.io/cloudnative-pg/postgresql:18.0@sha256:f06cae6ae14e2f101392130dce800b504bf9c5110b5db5fc0266782464882dbb",
        command: ["sh", "-ec"],
        args: ['psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -Atc "CREATE TABLE sdk_fixture (value integer); INSERT INTO sdk_fixture VALUES (117); SELECT value FROM sdk_fixture" | tail -1 | grep -qx 117; echo sdk-database-query-ok; exec sleep 3600'],
        bindings: { DATABASE_URL: binding },
        networks: ["backend"],
      }),
    },
    networks: {
      backend: {
        internal: true,
        virtual_network: networkName,
        segment: "backend",
      },
    },
  });
  const deploy = await review.apply();
  application = hako.application(deploy.applicationId);
  await deploy.wait({ timeoutMs: 3 * 60_000 });
  const current = await application.get();
  assert.equal(current.revision, 1);
  const change = await application.service("web").plan({ replicas: 2 });
  const scaled = await change.apply();
  await scaled.wait({ timeoutMs: 3 * 60_000 });
  await assert.rejects(
    application.service("web").scale(1, { expectedRevision: current.revision }),
    (error) => error instanceof APIError && error.isConflict,
  );
  const runtime = await application.service("web").runtime();
  assert(runtime);
  const logs = await application.service("web").logs({ tail: 10 });
  assert.equal(typeof logs, "string");
  assert((await application.service("checker").logs({ tail: 10 })).includes("sdk-database-query-ok"));
  assert.equal((await application.service("checker").get()).replicas, 1);
  console.log(
    JSON.stringify({
      deployment: deploy.id,
      scaled: scaled.id,
      database: creation.databaseId,
      status: "succeeded",
    }),
  );
} catch (error) {
  failures.push(error);
} finally {
  if (application)
    await cleanup(async () => {
      const empty = await application.plan({ services: {} });
      await (await empty.apply()).wait({ timeoutMs: 3 * 60_000 });
      const current = await application.get();
      await application.delete({
        confirmName: appName,
        expectedRevision: current.revision,
      });
    });
  if (database)
    await cleanup(async () => {
      const current = await database.get();
      const deletion = await database.delete({
        expectedRevision: current.revision,
        confirmName: current.spec.name,
      });
      await deletion.wait({ timeoutMs: 3 * 60_000 });
      assert.equal((await deletion.get()).status, "succeeded");
      await assert.rejects(
        database.get(),
        (error) => error instanceof APIError && error.status === 404,
      );
    });
  if (networkCreated)
    await cleanup(async () => {
      const current = await network.get();
      await network.delete({
        expectedId: current.network.id,
        expectedRevision: current.network.revision,
        confirmName: networkName,
      });
    });
}
if (failures.length)
  throw new AggregateError(failures, "SDK acceptance or cleanup failed");
