import { Hakopod, app, db, service } from "../src/index.js";
const client = new Hakopod({
  apiUrl: "https://fixture.invalid",
  apiKey: "hp_fixture",
}).in({ project: "fixture", environment: "development" });
client.request("GET", "/applications", {
  query: { project: "fixture", environment: "development" },
});
client.request("POST", "/databases", {
  body: {
    project: "fixture",
    environment: "development",
    spec: db({
      name: "fixture",
      engine: "postgresql",
      cpu: "100m",
      memory: "256Mi",
      storageGiB: 1,
    }),
  },
  idempotencyKey: "fixture-key",
});
client.plan(
  app({
    name: "fixture",
    services: { web: service({ image: "fixture.invalid/app", port: 8080 }) },
  }),
);
client.app("fixture").service("web").scale(2, { expectedRevision: 1 });
// @ts-expect-error Scope query is required by the server contract.
client.request("GET", "/applications");
// @ts-expect-error The route does not exist.
client.request("GET", "/imaginary");
// @ts-expect-error This method is not defined on this route.
client.request("DELETE", "/plan");
client.request("POST", "/deployments", {
  // @ts-expect-error A deployment requires the reviewed revision.
  body: { project: "fixture", environment: "development" },
});
// @ts-expect-error A service action must carry an expected revision.
client.app("fixture").service("web").scale(2);
// @ts-expect-error Misspelled configuration must not silently become an SDK option.
service({ image: "fixture.invalid/app", replica: 2 });
db({
  name: "fixture",
  // @ts-expect-error Database engine must be supported.
  engine: "mysql",
  cpu: "100m",
  memory: "256Mi",
  storageGiB: 1,
});
// @ts-expect-error A path ID must be supplied.
client.request("GET", "/database-operations/{id}", {});
