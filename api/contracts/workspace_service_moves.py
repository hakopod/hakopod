"""Keep the existing service-move contract reproducible during generation."""
schemas["ServiceMoveInput"] = obj({"destination_id": S, "destination_service": S, "source_revision": I, "destination_revision": I}, ["destination_id", "destination_service", "source_revision", "destination_revision"])
schemas["ServiceMovePlan"] = obj({"source": ref("Spec"), "destination": ref("Spec"), "source_changes": array(ref("Change")), "destination_changes": array(ref("Change")), "warnings": array(S), "secret_count": I}, ["source", "destination", "source_changes", "destination_changes", "warnings", "secret_count"])
schemas["ServiceMove"] = obj({"id": S, "source_id": S, "destination_id": S, "service": S, "destination_service": S, "removal_id": S, "status": S, "source_revision": I, "destination_revision": I}, ["id", "source_id", "destination_id", "service", "destination_service", "removal_id", "status", "source_revision", "destination_revision"])
for path, operation, response, status, idem in [
    ("/applications/{id}/services/{service}/move-plan", "planServiceMove", "ServiceMovePlan", "200", False),
    ("/applications/{id}/services/{service}/move", "startServiceMove", "Deployment", "202", True),
]:
    route(path, "post", operation, ref(response), ref("ServiceMoveInput"), status, idem=idem)
    paths[path]["post"]["parameters"].insert(1, {"name": "service", "in": "path", "required": True, "schema": S})
route("/applications/{id}/service-moves", "get", "listServiceMoves", items("ServiceMove"))
path = "/applications/{id}/service-moves/{move}/finish"
route(path, "post", "finishServiceMove", ref("Deployment"), status="202", idem=True)
paths[path]["post"]["parameters"].insert(1, {"name": "move", "in": "path", "required": True, "schema": S})
