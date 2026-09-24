"""Explicit durable reclamation of deleted applications' persistent data."""
schemas["RetainedApplicationData"] = obj({"application_id":S,"project":S,"environment":S,"name":S,"status":S,"error":S,"reserved_gib":I},["application_id","project","environment","name","status","error","reserved_gib"])
route("/storage/retained","get","listRetainedApplicationData",items("RetainedApplicationData"),scope=True)
route("/storage/retained/{id}","delete","deleteRetainedApplicationData",obj({"status":S},["status"]),obj({"confirm_name":S},["confirm_name"]),"202")
paths["/applications/{id}"]["delete"]["requestBody"]["content"]["application/json"]["schema"]["properties"]["delete_data"] = B
