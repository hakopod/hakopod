project_name = {"type": "string", "minLength": 1, "maxLength": 80}
description = {"type": "string", "maxLength": 1000}
schemas["Project"]["properties"].update({"display_name": project_name, "description": description, "personal": {"type": "boolean", "readOnly": True, "description": "Personal projects cannot be shared through invitations or team membership."}})
request = paths["/projects"]["post"]["requestBody"]["content"]["application/json"]["schema"]
request["properties"].update({"display_name": project_name, "description": description})
response = paths["/projects"]["post"]["responses"]["201"]["content"]["application/json"]["schema"]
response["properties"].update({"display_name": project_name, "description": description})
