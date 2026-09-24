"""Explicit Pro roles and organization MFA; personal factors remain Free."""
schemas['ProjectRole']['properties'].update({'permissions':array(S),'name':S})
schemas['Principal']['properties'].update({'mfa_verified':B,'mfa_required':B})
schemas['AccountSecurity']['properties'].update({'mfa_verified':B,'mfa_required':B,'organization_mfa_required':B})
schemas['CustomRole']=obj({'id':S,'name':S,'permissions':array(S),'revision':I},['id','name','permissions','revision'])
schemas['OrganizationSecurity']=obj({'require_mfa':B,'revision':I,'members':I,'ready_members':I},['require_mfa','revision','members','ready_members'])
role_input=obj({'name':S,'permissions':array(S),'expected_revision':I},['name','permissions','expected_revision'])
route('/roles','get','listCustomRoles',items('CustomRole'))
route('/roles','post','createCustomRole',ref('CustomRole'),role_input)
route('/roles/{role}','put','updateCustomRole',ref('CustomRole'),role_input)
route('/roles/{role}','delete','deleteCustomRole',obj({'deleted':B},['deleted']),obj({'expected_revision':I},['expected_revision']))
route('/organization/security','get','getOrganizationSecurity',ref('OrganizationSecurity'))
route('/organization/security','put','updateOrganizationSecurity',ref('OrganizationSecurity'),obj({'require_mfa':B,'expected_revision':I},['require_mfa','expected_revision']))
route('/auth/mfa/verify','post','verifySessionMFA',obj({'verified':B},['verified']),obj({'code':S},['code']))

for method in ("put", "delete"):
    paths["/roles/{role}"][method]["parameters"].append({"name":"role","in":"path","required":True,"schema":S})
