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
  engine: "unimplemented-engine",
  cpu: "100m",
  memory: "256Mi",
  storageGiB: 1,
});
db({
  name: "mysql",
  engine: "mysql",
  mode: "cluster",
  cpu: "500m",
  memory: "1Gi",
  storageGiB: 5,
  placement: { spread: "zones" },
});
client.database("fixture").binding({ endpoint: "pooled_read_write" });
// @ts-expect-error A path ID must be supplied.
client.request("GET", "/database-operations/{id}", {});
client.database("fixture").switchoverPlan("standby-pod");
client.database("fixture").switchover({ reviewId: "review", expectedRevision: 1, confirmName: "orders" });
client.database("fixture").retrySwitchover({ operationId: "operation", expectedRevision: 1, confirmName: "orders" });
// @ts-expect-error Switchover requires an explicit database-name confirmation.
client.database("fixture").switchover({ reviewId: "review", expectedRevision: 1 });
client.request("POST", "/databases/{id}/switchover", {
  params: { id: "fixture" },
  // @ts-expect-error A new standby cannot replace the reviewed target at submission.
  body: { review_id: "review", expected_revision: 1, confirm_name: "orders", target_member: "other" },
});
client.request("POST", "/databases/{id}/switchover-retry", {
  params: { id: "fixture" },
  body: { operation_id: "operation", expected_revision: 1, confirm_name: "orders" },
}).then((operation) => { const nativeTarget: string | undefined = operation.switchover?.target_unique_name; void nativeTarget; });

client.request("GET", "/managed-platforms");
declare const platformSpec: import("../src/api.generated.js").components["schemas"]["ManagedPlatformSpec"];
if (platformSpec.kind === "neon") {
  const computes: number = platformSpec.neon.compute_replicas;
  void computes;
  // @ts-expect-error Neon cannot carry a Supabase configuration.
  platformSpec.supabase;
} else {
  const publicUrl: string = platformSpec.supabase.public_url;
  void publicUrl;
  // @ts-expect-error Supabase cannot carry a Neon configuration.
  platformSpec.neon;
}
