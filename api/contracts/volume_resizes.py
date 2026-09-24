"""Reviewed offline filesystem resizing in every engine installation mode."""
schemas["VolumeResizeInput"] = obj({"claim":S,"size_gib":I,"expected_revision":I,"source_uid":S,"confirm_downtime":B},["claim","size_gib","expected_revision"])
schemas["VolumeResizePlan"] = obj({"claim":S,"old_gib":I,"size_gib":I,"temporary_gib":I,"peak_gib":I,"services":array(S),"source_uid":S,"expected_revision":I,"warnings":array(S)},["claim","old_gib","size_gib","temporary_gib","peak_gib","services","source_uid","expected_revision","warnings"])
schemas["VolumeResize"] = obj({"id":S,"application_id":S,"claim":S,"target_claim":S,"size_gib":I,"old_gib":I,"expected_revision":I,"phase":S,"error":S,"services":array(S),"created_at":T,"updated_at":T,"verified":B,"copied_bytes":I},["id","application_id","claim","target_claim","size_gib","old_gib","expected_revision","phase","error","services","created_at","updated_at","verified","copied_bytes"])
route("/applications/{id}/volume-resizes/plan","post","planVolumeResize",ref("VolumeResizePlan"),ref("VolumeResizeInput"),idem=True)
route("/applications/{id}/volume-resizes","post","startVolumeResize",ref("VolumeResize"),ref("VolumeResizeInput"),"202",idem=True)
route("/applications/{id}/volume-resizes","get","listVolumeResizes",items("VolumeResize"))
path="/applications/{id}/volume-resizes/{resize}/{action}"
route(path,"post","volumeResizeAction",obj({"status":S},["status"]),obj({"confirm_claim":S}),"202")
paths[path]["post"]["parameters"] += [{"name":"resize","in":"path","required":True,"schema":S},{"name":"action","in":"path","required":True,"schema":{"type":"string","enum":["retry","cancel","retain","delete-original"]}}]
