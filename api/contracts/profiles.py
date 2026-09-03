import re

schemas['Profile'] = obj({'name':S,'avatar_style':{'type':'string','enum':['initials','identicon','glass']},'avatar_seed':S,'avatar_url':S,'revision':I},['name','avatar_style','avatar_seed','avatar_url','revision'])
schemas['ProfileInput'] = obj({'name':S,'avatar_style':{'type':'string','enum':['initials','identicon','glass']},'avatar_seed':S,'expected_revision':I},['name','avatar_style','expected_revision'])
schemas['HostPermission'] = obj({'node':S,'permission':S},['node','permission'])
schemas['HostGrant'] = obj({'identity_id':S,'node':S,'permission':S,'expires_at':T},['identity_id','node','permission','expires_at'])
schemas['HostAccess'] = obj({'super_admin':B,'grants':array(ref('HostGrant')),'nodes':array(obj({'name':S,'control_plane':B,'ready':B,'allowed':B},['name','control_plane','ready','allowed']))},['super_admin','grants','nodes'])
schemas['Principal']['properties'].update({'avatar_style':S,'avatar_seed':S,'avatar_url':S,'profile_revision':I,'host_permissions':array(ref('HostPermission'))})
schemas['TeamMember']['properties'].update({'username':S,'avatar_url':S})
schemas['TeamMember']['required'] += ['username','avatar_url']
route('/auth/profile','get','getProfile',ref('Profile'))
route('/auth/profile','patch','updateProfile',ref('Profile'),ref('ProfileInput'))
route('/teams/{id}/members/{user}/username','put','setTeamUsername',ref('TeamMember'),obj({'username':S},['username']))
route('/host-access','get','getHostAccess',ref('HostAccess'))
route('/host-access/{user}','put','grantHostAccess',ref('HostGrant'),obj({'node':S,'permission':S,'expires_at':T},['node','permission','expires_at']))
route('/host-access/{user}/{node}','delete','revokeHostAccess',obj({'revoked':B},['revoked']))
route('/nodes/{node}/terminal','post','openHostTerminal',obj({'id':S,'expires_at':T,'node':S},['id','expires_at','node']),obj({'cols':I,'rows':I}),'201')
route('/nodes/{node}/terminal/{session}/output','get','streamHostTerminal',S)
paths['/nodes/{node}/terminal/{session}/output']['get']['responses']['200']['content']={'text/event-stream':{'schema':S}}
route('/nodes/{node}/terminal/{session}/input','post','writeHostTerminal',{},obj({'data':S,'cols':I,'rows':I}),'204')
route('/nodes/{node}/terminal/{session}','delete','closeHostTerminal',{},None,'204')
for path in ['/teams/{id}/members/{user}/username','/host-access/{user}','/host-access/{user}/{node}','/nodes/{node}/terminal','/nodes/{node}/terminal/{session}/output','/nodes/{node}/terminal/{session}/input','/nodes/{node}/terminal/{session}']:
    for operation in paths[path].values():
        for name in re.findall(r'\{([^}]+)\}',path):
            if name!='id':operation['parameters'].append({'name':name,'in':'path','required':True,'schema':S})
