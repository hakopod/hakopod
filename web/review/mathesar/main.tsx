import React from 'react'
import {createRoot} from 'react-dom/client'
import {QueryClient,QueryClientProvider} from '@tanstack/react-query'
import {createRootRoute,createRoute,createRouter,Outlet,RouterProvider} from '@tanstack/react-router'
import '../../src/styles.css'
import catalog from '../../../.local/mathesar-ui-review/catalog-fixture.json'
import {setTheme} from '../../src/lib/appearance'

// DEVELOPMENT FIXTURE: real catalog metadata and Go-generated template plan;
// identity, project, secret metadata and request failures are artificial.
const params=new URLSearchParams(location.search)
setTheme(params.get('theme')==='light'?'light':'dark')
const now=new Date().toISOString()
const viewer=params.has('viewer')
const identity={id:'fixture-user',name:'Development UI reviewer',email:'fixture@example.invalid',admin:!viewer,owner:false,credential_type:'browser',permissions:['deployments:read',...(viewer?[]:['deployments:write'])],project_roles:[{project:'fixture-review',role:viewer?'viewer':'admin'}],host_permissions:[],avatar_url:''}
const fixture={requests:[] as unknown[],planMode:'success',secretMode:'success',deployMode:'error',savedSecrets:[] as string[],catalogMode:params.get('state')||'success'}
const originalFetch=window.fetch.bind(window)
Object.assign(window,{__fixture:fixture,__fixtureSetTheme:setTheme})
window.fetch=async(input:RequestInfo|URL,init?:RequestInit)=>{
 const request=input instanceof Request?input:new Request(new URL(String(input),location.origin),init)
 const url=new URL(request.url),method=init?.method||request.method
 if(url.origin!==location.origin)throw new Error(`Development fixture blocked external request: ${url.origin}`)
 if(!url.pathname.startsWith('/api/')&&!url.pathname.startsWith('/session'))return originalFetch(input,init)
 let data:any,status=200,body:any
 if(method==='GET'){
  if(url.pathname==='/api/me')data=identity
  else if(url.pathname==='/api/projects')data={items:[{id:'fixture-project',name:'fixture-review',display_name:'Development UI fixture',description:'Artificial review project',personal:false,environments:[{name:'development'}]}]}
  else if(url.pathname==='/api/templates'){
   if(fixture.catalogMode==='error'){status=503;data={error:{code:'fixture_unavailable',message:'Development fixture: catalog is unavailable.'}}}
   else data={items:fixture.catalogMode==='empty'?[]:catalog.items}
  }
  else if(url.pathname==='/api/secrets')data={items:fixture.savedSecrets.map(name=>({name,application:'fixture-mathesar',updated_at:now}))}
  else if(url.pathname==='/api/license')data={valid:false,edition:'community',catalog:[]}
  else if(url.pathname==='/api/auth/status')data={deployment_mode:'self-hosted',setup_required:false,signup_enabled:false,password:true,providers:[],passkeys:true,totp:true,email_delivery:false}
  else if(url.pathname==='/api/alarms')data={items:[],summary:{active:0,unread:0},truncated:false}
  else if(url.pathname==='/api/applications')data={items:[]}
  else if(url.pathname==='/api/teams')data={items:[]}
 }
 if(method==='POST'&&url.pathname==='/api/templates/mathesar/plan'){
  body=await request.json()
  if(fixture.planMode==='error'){status=400;data={error:{code:'fixture_invalid',message:'Development fixture: review request failed. Your entries are preserved.'}}}
  else{
   const key=`${body.values['database-mode']||'bundled'}-${body.values['media-storage-class']?'rwx':'local'}`
   const selected=catalog.plans[key as keyof typeof catalog.plans]
   if(!selected)throw new Error(`Development fixture has no plan: ${key}`)
   const expected=selected.configuration
   for(const field of ['name','storage_gib','architecture','site_url'] as const){
    if(body[field]!==expected[field])throw new Error(`Development fixture configuration mismatch: ${field}`)
   }
   if(JSON.stringify(Object.entries(body.values).sort())!==JSON.stringify(Object.entries(expected.values).sort()))throw new Error('Development fixture values differ from the generated plan')
   data=structuredClone(selected);data.configuration=body
  }
 }
 if(method==='PUT'&&url.pathname.startsWith('/api/templates/mathesar/secrets/')){
  body=await request.json();const name=url.pathname.split('/').at(-1)!
  if(fixture.secretMode==='error'){status=400;data={error:{code:'fixture_invalid',message:'Development fixture: secret was not saved. Your entry is preserved.'}}}
  else{fixture.savedSecrets.push(name);data={name,saved:true}}
 }
 if(method==='POST'&&url.pathname==='/api/templates/mathesar/deploy'){
  body=await request.json();status=503;data={error:{code:'fixture_failed',message:'Development fixture: deployment was not applied. The review is preserved.'}}
 }
 // Secret request bodies are never retained, even though review values are synthetic.
 const recordedBody=method==='PUT'?{redacted:true}:body
 fixture.requests.push({method,path:url.pathname,status,allowed:data!==undefined,...(recordedBody?{body:recordedBody}:{})})
 if(fixture.requests.length>128)fixture.requests.shift()
 if(data===undefined)throw new Error(`Development fixture blocked request: ${method} ${url.pathname}`)
 return new Response(JSON.stringify(data),{status,headers:{'Content-Type':'application/json'}})
}
const {DashboardShell}=await import('../../src/components/shell')
const query=new QueryClient({defaultOptions:{queries:{retry:false,refetchOnWindowFocus:false,gcTime:0}}})
const root=createRootRoute({component:()=> <DashboardShell><Outlet/></DashboardShell>})
const catalogRoute=(await import('../../src/routes/templates')).Route
const detailRoute=(await import('../../src/routes/templates.$templateId')).Route
catalogRoute.update({id:'/templates',path:'/templates',getParentRoute:()=>root} as never)
detailRoute.update({id:'/$templateId',path:'/$templateId',getParentRoute:()=>catalogRoute} as never)
const router=createRouter({routeTree:root.addChildren([catalogRoute.addChildren([detailRoute])]),defaultPreload:false})
createRoot(document.getElementById('root')!).render(<QueryClientProvider client={query}><RouterProvider router={router}/></QueryClientProvider>)
