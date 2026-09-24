"""Human identity, browser sessions and scoped device authorization."""
import re

schemas["ProjectRole"] = obj({"project": S, "role": S}, ["project", "role"])
schemas["Principal"]["properties"].update({"email": S, "owner": B, "credential_type": {"type":"string","enum":["machine","browser","cli","integration"]}, "project_roles": array(ref("ProjectRole"))})
schemas["Principal"]["required"] += ["owner", "credential_type"]
schemas["AuthStatus"] = obj({"setup_required":B,"password":B,"providers":array(S),"passkeys":B,"totp":B,"email_delivery":B,"signup_enabled":B,"password_recovery":B},["setup_required","password","providers","passkeys","totp","email_delivery","signup_enabled","password_recovery"])
schemas["AuthStatus"]["properties"]["deployment_mode"] = {"type": "string", "enum": ["self-hosted", "managed-cloud"], "description": "Effective validated deployment mode, including before first-owner setup. This does not enable public signup."}
schemas["AuthStatus"]["required"].append("deployment_mode")
schemas["HumanSessionCreated"] = obj({"token":S,"user":ref("Principal"),"expires_at":T,"onboarding_required":B},["token","user","expires_at","onboarding_required"])
schemas["HumanSession"] = obj({"id":S,"kind":S,"current":B,"created_at":T,"expires_at":T,"last_used_at":{"anyOf":[T,{"type":"null"}]}},["id","kind","current","created_at","expires_at","last_used_at"])
schemas["User"] = obj({"id":S,"name":S,"email":S,"admin":B,"owner":B,"disabled":B,"email_verified":B,"totp_enabled":B,"created_at":T},["id","name","email","admin","owner","disabled","email_verified","totp_enabled","created_at"])
schemas["Team"] = obj({"id":S,"name":S,"role":S},["id","name","role"])
schemas["TeamMember"] = obj({"id":S,"name":S,"email":S,"role":S},["id","name","email","role"])
schemas["ProjectMember"] = obj({"identity_id":S,"team_id":S,"name":S,"role":S},["name","role"])
schemas["Invite"] = obj({"id":S,"email":S,"team_id":S,"project":S,"role":S,"expires_at":T},["id","email","role","expires_at"])
schemas["InviteCreated"] = obj({"invite":ref("Invite"),"invite_url":S,"delivered":B},["invite","invite_url","delivered"])
schemas["Passkey"] = obj({"id":S,"name":S,"created_at":T,"last_used_at":{"anyOf":[T,{"type":"null"}]}},["id","name","created_at","last_used_at"])
schemas["AccountSecurity"] = obj({"totp_enabled":B,"password_enabled":B,"recovery_codes_remaining":I,"passkeys":array(ref("Passkey"))},["totp_enabled","password_enabled","recovery_codes_remaining","passkeys"])
schemas["PasskeyChallenge"] = obj({"challenge":S,"options":obj({"publicKey":mapping({})},["publicKey"])},["challenge","options"])
schemas["DeviceAuthorization"] = obj({"device_code":S,"user_code":S,"verification_uri":S,"verification_uri_complete":S,"expires_in":I,"interval":I},["device_code","user_code","verification_uri","verification_uri_complete","expires_in","interval"])
schemas["DeviceScope"] = obj({"id":S,"label":S,"project":S,"environment":S},["id","label","project","environment"])
schemas["DeviceDetails"] = obj({"user_code":S,"project":S,"environment":S,"permissions":array(S),"expires_at":T,"scope_id":S,"scopes":array(ref("DeviceScope"))},["user_code","project","environment","permissions","expires_at","scope_id","scopes"])
schemas["DeviceToken"] = obj({"token":S,"access_token":S,"scope_id":S,"token_type":S,"expires_at":T,"expires_in":I,"user":ref("Principal")},["token","access_token","token_type","expires_at","expires_in","user"])

new_paths = set()
def authroute(path, method, operation, response, request=None, status="200", public=False):
    route(path,method,operation,response,request,status)
    item=paths[path][method]
    for name in re.findall(r"\{([^}]+)\}",path):
        if name!="id":item["parameters"].append({"name":name,"in":"path","required":True,"schema":S})
    if public:item["security"]=[]
    new_paths.add(path)

authroute("/auth/status","get","getAuthStatus",ref("AuthStatus"),public=True)
authroute("/auth/setup","post","setupInstallerOwner",ref("HumanSessionCreated"),obj({"name":S,"email":S,"password":S,"setup_token":S},["name","email","password"]),public=True)
authroute("/auth/login","post","loginWithPassword",ref("HumanSessionCreated"),obj({"email":S,"password":S,"code":S},["email","password"]),public=True)
authroute("/auth/logout","post","logoutHuman",obj({"logged_out":B},["logged_out"]),obj({}))
authroute("/auth/sessions","get","listHumanSessions",items("HumanSession"))
authroute("/auth/sessions/{id}","delete","revokeHumanSession",obj({"revoked":B},["revoked"]))
authroute("/auth/security","get","getAccountSecurity",ref("AccountSecurity"))
authroute("/auth/mfa/totp/start","post","beginTOTP",obj({"challenge":S,"secret":S,"otpauth_url":S},["challenge","secret","otpauth_url"]),obj({"password":S},["password"]))
authroute("/auth/mfa/totp/confirm","post","confirmTOTP",obj({"enabled":B,"recovery_codes":array(S)},["enabled","recovery_codes"]),obj({"challenge":S,"code":S},["challenge","code"]))
authroute("/auth/mfa/totp/disable","post","disableTOTP",obj({"disabled":B},["disabled"]),obj({"password":S,"code":S},["password","code"]))
authroute("/auth/mfa/complete","post","completeProviderMFA",ref("HumanSessionCreated"),obj({"challenge":S,"code":S},["challenge","code"]),public=True)
authroute("/auth/invites/accept","post","acceptHumanInvite",ref("HumanSessionCreated"),obj({"token":S,"name":S,"password":S,"workspace":{"type":"string","enum":["invite","personal"]}},["token"]),public=True)
authroute("/auth/device/start","post","startDeviceAuthorization",ref("DeviceAuthorization"),obj({"project":S,"environment":S,"permissions":array(S)}),public=True)
authroute("/auth/device/token","post","pollDeviceAuthorization",ref("DeviceToken"),obj({"device_code":S},["device_code"]),public=True)
authroute("/auth/device","get","getDeviceConsent",ref("DeviceDetails"))
paths["/auth/device"]["get"]["parameters"].append({"name":"user_code","in":"query","required":True,"schema":S})
authroute("/auth/device/approve","post","approveDeviceAuthorization",obj({"approved":B},["approved"]),obj({"user_code":S,"approve":B,"project":S,"environment":S,"scope_id":S},["user_code","approve"]))
authroute("/auth/oauth/{provider}/start","get","startProviderLogin",obj({}),status="302",public=True)
authroute("/auth/oauth/{provider}/callback","get","finishProviderLogin",{"oneOf":[ref("HumanSessionCreated"),obj({"mfa_required":B,"challenge":S},["mfa_required","challenge"])]},public=True)
paths["/auth/oauth/{provider}/callback"]["get"]["parameters"] += [{"name":v,"in":"query","schema":S} for v in ["state","code","error"]]
for path in ["/auth/oauth/{provider}/start","/auth/oauth/{provider}/callback"]:
    for parameter in paths[path]["get"]["parameters"]:
        if parameter["name"] == "provider":parameter["schema"]={"type":"string","enum":["github","google","gitlab","oidc"]}
authroute("/auth/passkeys/register/start","post","beginPasskeyRegistration",ref("PasskeyChallenge"),obj({"name":S,"password":S,"code":S},["name","password"]))
authroute("/auth/passkeys/register/finish","post","finishPasskeyRegistration",obj({"id":S,"name":S},["id","name"]),obj({"challenge":S,"credential":mapping({})},["challenge","credential"]),"201")
authroute("/auth/passkeys/login/start","post","beginPasskeyLogin",ref("PasskeyChallenge"),obj({}),public=True)
authroute("/auth/passkeys/login/finish","post","finishPasskeyLogin",ref("HumanSessionCreated"),obj({"challenge":S,"credential":mapping({})},["challenge","credential"]),public=True)
authroute("/auth/passkeys/{id}","delete","deletePasskey",obj({"deleted":B},["deleted"]),obj({"password":S,"code":S},["password"]))
authroute("/users","get","listUsers",items("User"))
authroute("/users/{id}","patch","updateUser",obj({"updated":B},["updated"]),obj({"disabled":B,"admin":B},["disabled","admin"]))
authroute("/teams","get","listTeams",items("Team"))
authroute("/teams","post","createTeam",ref("Team"),obj({"name":S},["name"]),"201")
authroute("/teams/{id}","delete","deleteTeam",obj({"deleted":B},["deleted"]))
authroute("/teams/{id}/members","get","listTeamMembers",items("TeamMember"))
authroute("/teams/{id}/members/{user}","put","setTeamMember",obj({"updated":B},["updated"]),obj({"role":S},["role"]))
authroute("/teams/{id}/invites","post","createTeamInvite",ref("InviteCreated"),obj({"email":S,"role":S,"project":S,"deliver":B},["email","role"]),"201")
authroute("/projects/{project}/invites","post","createProjectInvite",ref("InviteCreated"),obj({"email":S,"role":S,"deliver":B},["email","role"]),"201")
authroute("/projects/{project}/members","get","listProjectMembers",items("ProjectMember"))
authroute("/projects/{project}/members","put","setProjectMember",obj({"updated":B},["updated"]),obj({"identity_id":S,"team_id":S,"role":S},["role"]))

schemas["OnboardingInvite"] = obj({"id":S,"team_name":S,"project":S,"role":S,"expires_at":T},["id","team_name","project","role","expires_at"])
schemas["Onboarding"] = obj({"required":B,"personal_project":S,"invitations":array(ref("OnboardingInvite"))},["required","personal_project","invitations"])
authroute("/auth/register","post","registerAccount",obj({"accepted":B},["accepted"]),obj({"name":S,"email":S,"password":S},["name","email","password"]),status="202",public=True)
authroute("/auth/register/verify","post","verifyRegistration",ref("HumanSessionCreated"),obj({"token":S},["token"]),public=True)
authroute("/auth/password/forgot","post","requestPasswordReset",obj({"accepted":B},["accepted"]),obj({"email":S},["email"]),status="202",public=True)
authroute("/auth/password/reset","post","resetPassword",obj({"reset":B},["reset"]),obj({"token":S,"password":S},["token","password"]),public=True)
authroute("/auth/invites/inspect","post","inspectInvite",ref("Invite"),obj({"token":S},["token"]),public=True)
authroute("/auth/onboarding","get","getOnboarding",ref("Onboarding"))
authroute("/auth/onboarding","post","completeOnboarding",ref("HumanSessionCreated"),obj({"choice":{"type":"string","enum":["invite","personal"]},"invite_id":S},["choice"]))
paths["/auth/oauth/{provider}/start"]["get"]["parameters"] += [{"name":v,"in":"query","schema":S} for v in ["intent","invite_token"]]
