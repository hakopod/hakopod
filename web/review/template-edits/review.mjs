import assert from 'node:assert/strict'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import { fileURLToPath, pathToFileURL } from 'node:url'

const project=fileURLToPath(new URL('../../../',import.meta.url))
const output=process.env.HAKOPOD_TEMPLATE_REVIEW_OUTPUT||`${project}/.local/template-edit-review/evidence`
const base=`http://127.0.0.1:${Number(process.env.HAKOPOD_TEMPLATE_REVIEW_PORT||4198)}`
const {chromium}=await import(pathToFileURL(process.env.HAKOPOD_PLAYWRIGHT_MODULE).href)
const browser=await chromium.launch({headless:true,args:['--disable-dev-shm-usage']})
const reviewedFiles=['web/src/components/template-form.tsx','web/src/components/template-secret-field.tsx','web/src/routes/templates.$templateId.tsx','web/src/lib/template-review.ts','web/src/components/toml-editor.tsx','web/src/lib/editor-schema.json','web/src/lib/toml-language.ts']
const hash=async()=>Object.fromEntries(await Promise.all(reviewedFiles.map(async path=>[path,createHash('sha256').update(await readFile(`${project}/${path}`)).digest('hex')])))
const initialHashes=await hash()
const cases=[],captures=[]
const filter=process.env.HAKOPOD_TEMPLATE_REVIEW_CASES?new RegExp(process.env.HAKOPOD_TEMPLATE_REVIEW_CASES):null
const customSecret='ARTIFICIAL MULTILINE SECRET\nSecond line for preservation only'
await mkdir(`${output}/screenshots`,{recursive:true})

async function settled(page,locator){
  await page.waitForFunction(()=>window.__templateFixture?.ready)
  await locator.waitFor({state:'visible'})
  await page.locator('.hako-loading-stack').first().waitFor({state:'hidden'})
  await page.evaluate(()=>document.fonts.ready)
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))
}
async function capture(page,id){
  await page.evaluate(()=>window.scrollTo(0,0));await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))
  const geometry=await page.evaluate(()=>{
    const rect=el=>{const r=el.getBoundingClientRect();return{x:r.x,y:r.y,right:r.right,bottom:r.bottom,width:r.width,height:r.height}}
    const visible=el=>{const r=el.getBoundingClientRect();return r.width>0&&r.height>0&&getComputedStyle(el).visibility!=='hidden'}
    const main=document.querySelector('main'),head=document.querySelector('.hako-page-heading'),style=getComputedStyle(main)
    return{viewport:innerWidth,documentWidth:Math.max(document.body.scrollWidth,document.documentElement.scrollWidth),main:rect(main),insets:[style.paddingLeft,style.paddingRight],
      headings:[...main.querySelectorAll('h1')].map(el=>({text:el.textContent,box:rect(el)})),
      heading:head?{box:rect(head),top:getComputedStyle(head).paddingTop,bottom:getComputedStyle(head).paddingBottom}:null,
      controls:[...main.querySelectorAll('input,textarea,button,[role=combobox],a')].filter(visible).filter(el=>!el.closest('.monaco-editor')).map(el=>({name:el.getAttribute('aria-label')||el.textContent||el.id,box:rect(el)})),
      editor:[...main.querySelectorAll('.monaco-editor')].filter(visible).map(el=>({box:rect(el),theme:el.className})),
      brackets:[...document.querySelectorAll('.brackets')].filter(visible).length,
      blocked:window.__templateFixture.blocked,artificial:window.__templateFixture.artificial,
      secretNames:[...main.querySelectorAll('.service-summary-panel .settings-list-row strong')].map(el=>el.textContent),
    }
  })
  assert(geometry.artificial);assert.deepEqual(geometry.blocked,[])
  assert(geometry.documentWidth<=geometry.viewport+1,`Document overflow ${geometry.documentWidth-geometry.viewport}px`)
  assert.deepEqual(geometry.insets,[geometry.viewport<640?'16px':'24px',geometry.viewport<640?'16px':'24px'])
  assert.equal(geometry.headings.length,1);assert.equal(geometry.brackets,0)
  if(geometry.heading)assert.equal(geometry.heading.top,geometry.heading.bottom,'Page heading padding is unbalanced')
  for(const control of [...geometry.controls,...geometry.editor])assert(control.box.x>=-1&&control.box.right<=geometry.viewport+1,`Clipped control: ${control.name||'TOML editor'}`)
  if(geometry.editor.length)geometry.markers=await page.evaluate(()=>window.__templateFixture.readMarkers())
  const screenshot=`screenshots/${id}.png`
  await page.screenshot({path:`${output}/${screenshot}`,fullPage:true})
  captures.push({id,screenshot,geometry})
}
async function choose(page,label,value){
  const control=page.getByRole('combobox',{name:label,exact:true});await control.focus();await control.press('ArrowDown')
  const opts=page.getByRole('option');await opts.first().waitFor();const names=await opts.allTextContents(),i=names.findIndex(n=>n.trim()===value)
  assert(i>=0);await opts.nth(i).focus();await page.keyboard.press('Enter');await control.filter({hasText:value}).waitFor()
}
async function configure(page,target){
  await settled(page,page.getByRole('heading',{name:'Configure Outpost',exact:true}))
  const name=page.getByRole('textbox',{name:'Application name',exact:true})
  if(target==='new')await name.fill('fixture-outpost');else{assert(!await name.isEnabled());assert.equal(await name.inputValue(),'fixture-outpost')}
  await choose(page,'Target architecture','Linux ARM64')
  await page.getByRole('button',{name:'Review template',exact:true}).click()
  await settled(page,page.getByRole('heading',{name:'Review fixture-outpost',exact:true}))
  await page.waitForFunction(()=>window.__templateFixture.requests.some(r=>r.path==='/api/secrets'))
}
async function openEditor(page,touch=false){
  const button=page.getByRole('button',{name:'Edit TOML',exact:true});await button.scrollIntoViewIfNeeded()
  if(touch)await button.tap();else{await button.focus();await button.press('Enter')}
  await settled(page,page.getByRole('heading',{name:'Edit fixture-outpost',exact:true}))
  await page.locator('.monaco-editor textarea.inputarea').waitFor({state:'attached'})
  await page.waitForFunction(async()=> (await window.__templateFixture.readEditor()).length===1)
}
async function edit(page,value){
  const input=page.locator('.monaco-editor textarea.inputarea');await input.focus();await input.press('ControlOrMeta+A');await page.keyboard.insertText(value)
  assert.equal(await page.evaluate(async()=> (await window.__templateFixture.readEditor())[0]),value)
}
async function run(name,target,theme,width,callback,scenario='normal'){
  const id=`${name}-${target}-${theme}-${width}`;if(filter&&!filter.test(id))return
  const context=await browser.newContext({viewport:{width,height:1000},hasTouch:width<640,isMobile:width<640,reducedMotion:'reduce'})
  await context.addInitScript(theme=>localStorage.setItem('hakopod-theme',theme),theme)
  const external=[]
  await context.route('**/*',route=>{if(new URL(route.request().url()).origin===base)return route.continue();external.push(route.request().url());return route.abort()})
  const page=await context.newPage(),errors=[];page.on('pageerror',error=>errors.push(error.message));page.setDefaultTimeout(30000)
  try{
    await page.goto(`${base}/templates/outpost?project=fixture-review&environment=development&fixture=${scenario}${target==='existing'?'&application=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa':''}`,{waitUntil:'domcontentloaded'})
    await callback(page,id)
    assert.deepEqual(external,[]);assert.deepEqual(errors,[])
    const ledger=await page.evaluate(()=>({requests:window.__templateFixture.requests,secretWrites:window.__templateFixture.secretWrites}))
    assert(!JSON.stringify(ledger).includes(customSecret),'Raw synthetic secret reached evidence ledger')
    cases.push({id,status:'passed',...ledger});console.log(`PASS ${id}`)
  }catch(error){
    const diagnostic=await page.evaluate(()=>({text:document.body.innerText,blocked:window.__templateFixture?.blocked})).catch(()=>null)
    await page.screenshot({path:`${output}/screenshots/${id}-FAILED.png`,fullPage:true}).catch(()=>{})
    cases.push({id,status:'failed',error:error.message,diagnostic,errors});console.log(`FAIL ${id}: ${error.message}`)
  }finally{await context.close()}
}

for(const theme of ['dark','light'])for(const width of [1484,390,320])for(const target of ['new','existing']){
  await run('edit-review-failure',target,theme,width,async(page,id)=>{
    await configure(page,target);await capture(page,`${id}-baseline`)
    const expected=await page.evaluate(()=>window.__templateFixture.catalog.plans[`${window.__templateFixture.target}-edited`])
    await openEditor(page,width<640);assert.deepEqual(await page.evaluate(()=>window.__templateFixture.readMarkers()),[],'Generated canonical TOML has diagnostics');await capture(page,`${id}-editor`)
    assert.equal(await page.getByRole('button',{name:'Deploy template',exact:true}).count(),0,'Editor exposes deployment')
    await edit(page,'schema_version = 1\nname = "fixture-outpost"\n[services.invalid')
    await page.evaluate(()=>{window.__templateFixture.planMode='error'})
    await page.getByRole('button',{name:'Review configuration',exact:true}).click()
    await settled(page,page.getByText('Artificial fixture: The edited TOML is invalid. Your edits are preserved.',{exact:true}).first())
    assert.equal(await page.evaluate(async()=> (await window.__templateFixture.readEditor())[0]),'schema_version = 1\nname = "fixture-outpost"\n[services.invalid')
    assert.equal(await page.evaluate(()=>window.__templateFixture.deployments.length),0)
    await capture(page,`${id}-invalid-preserved`)
    await edit(page,expected.toml)
    await page.evaluate(()=>{window.__templateFixture.planMode='success';window.__templateFixture.holdPlan=true})
    await page.getByRole('button',{name:'Review configuration',exact:true}).click()
    await page.waitForFunction(()=>Boolean(window.__templateFixture.releasePlan))
    assert(!await page.getByRole('button',{name:'Working…',exact:true}).isEnabled())
    const planCount=await page.evaluate(()=>window.__templateFixture.plans.length)
    await page.getByRole('button',{name:'Working…',exact:true}).evaluate(el=>el.click())
    assert.equal(await page.evaluate(()=>window.__templateFixture.plans.length),planCount)
    await page.evaluate(()=>{window.__templateFixture.holdPlan=false;window.__templateFixture.releasePlan()})
    await settled(page,page.getByRole('heading',{name:'Review fixture-outpost',exact:true}))
    await page.getByText('Custom configuration reviewed',{exact:true}).waitFor()
    const names=await page.locator('.service-summary-panel .settings-list-row strong').allTextContents()
    assert.deepEqual(names.toSorted(),expected.required_secrets.toSorted())
    assert(!names.includes('database-password')&&!names.includes('broker-password'),'Removed bundled secrets remain required')
    if(target==='existing'){
      const removed=page.locator('.diff-row').filter({has:page.locator('.diff-field span',{hasText:'previous-cache'})});
      for(let n=0;n<8&&!await removed.count();n++){const next=page.getByRole('button',{name:'Next',exact:true});assert(await next.isEnabled());await next.click()}
      await removed.waitFor();assert.equal((await removed.locator('.diff-before code').innerText()).trim(),'present');assert.equal((await removed.locator('.diff-after code').innerText()).trim(),'—')
    }
    await capture(page,`${id}-review`)
    const row=page.locator('.settings-list-row').filter({has:page.locator('strong',{hasText:'custom-api-key'})})
    await row.getByRole('button',{name:'Set value',exact:true}).click()
    const secret=page.getByRole('textbox',{name:'custom-api-key',exact:true})
    assert.equal(await secret.evaluate(el=>el.tagName),'TEXTAREA');await secret.fill(customSecret)
    await page.evaluate(()=>{window.__templateFixture.secretMode='error'})
    await page.getByRole('button',{name:'Save secret',exact:true}).click()
    await page.getByText('Artificial fixture: The secret could not be saved. Your entry is preserved.',{exact:true}).first().waitFor()
    assert.equal(await secret.inputValue(),customSecret);await capture(page,`${id}-secret-preserved`)
    await page.evaluate(()=>{window.__templateFixture.secretMode='success'})
    await page.getByRole('button',{name:'Save secret',exact:true}).click()
    await secret.waitFor({state:'hidden'});await page.getByRole('button',{name:'Deploy template',exact:true}).waitFor()
    await page.waitForFunction(()=>!document.querySelector('.form-footer button:last-child').disabled)
    const write=await page.evaluate(()=>window.__templateFixture.secretWrites.at(-1));assert.equal(write.method,'POST');assert.deepEqual(write.scope,{project:'fixture-review',environment:'development',application:'fixture-outpost'})
    await page.evaluate(()=>{window.__templateFixture.holdDeploy=true})
    await page.getByRole('button',{name:'Deploy template',exact:true}).click()
    await page.waitForFunction(()=>Boolean(window.__templateFixture.releaseDeploy))
    assert(!await page.getByRole('button',{name:'Working…',exact:true}).isEnabled())
    await page.getByRole('button',{name:'Working…',exact:true}).evaluate(el=>el.click())
    assert.equal(await page.evaluate(()=>window.__templateFixture.deployments.length),1)
    await page.evaluate(()=>{window.__templateFixture.releaseDeploy()})
    await page.getByText('Artificial fixture: Deployment was not applied. The reviewed configuration is preserved.',{exact:true}).first().waitFor()
    const sent=await page.evaluate(()=>window.__templateFixture.deployments[0]);assert.equal(sent.toml,expected.toml);assert.equal(sent.configuration.toml,expected.toml);assert.equal(sent.expected_revision,target==='existing'?7:0)
    await capture(page,`${id}-deploy-failed`)
    await openEditor(page,width<640);assert.equal(await page.evaluate(async()=> (await window.__templateFixture.readEditor())[0]),expected.toml);assert.deepEqual(await page.evaluate(()=>window.__templateFixture.readMarkers()),[],'Reviewed edited TOML has diagnostics')
    await edit(page,expected.toml+'\n# unsaved draft')
    await page.getByRole('button',{name:'Discard TOML edits',exact:true}).click()
    await openEditor(page,width<640);assert.equal(await page.evaluate(async()=> (await window.__templateFixture.readEditor())[0]),expected.toml)
  })
}
for(const theme of ['dark','light']){
  await run('async-target-change','existing',theme,390,async(page,id)=>{
    await configure(page,'existing');await openEditor(page,true)
    const edited=await page.evaluate(()=>window.__templateFixture.catalog.plans['existing-edited'].toml);await edit(page,edited)
    await page.evaluate(()=>{window.__templateFixture.holdPlan=true})
    await page.getByRole('button',{name:'Review configuration',exact:true}).click()
    await page.waitForFunction(()=>Boolean(window.__templateFixture.releasePlan))
    await page.evaluate(()=>{window.__templateFixture.application.revision++;window.__templateFixture.invalidate()})
    await page.getByText('The application or scope changed. Return to configuration and review it again before deploying.',{exact:true}).waitFor()
    await page.evaluate(()=>{window.__templateFixture.releasePlan()})
    await page.getByText('The application changed or the reviewed scope differs. Reload and review the template again.',{exact:true}).first().waitFor()
    assert.equal(await page.evaluate(async()=> (await window.__templateFixture.readEditor())[0]),edited)
    assert.equal(await page.evaluate(()=>window.__templateFixture.deployments.length),0)
    assert(!await page.getByRole('button',{name:'Review configuration',exact:true}).isEnabled())
    await capture(page,id)
  })
  await run('stale-response','existing',theme,390,async(page,id)=>{
    await configure(page,'existing');await openEditor(page,true)
    const edited=await page.evaluate(()=>window.__templateFixture.catalog.plans['existing-edited'].toml);await edit(page,edited)
    await page.evaluate(()=>{window.__templateFixture.planMode='stale'})
    await page.getByRole('button',{name:'Review configuration',exact:true}).click()
    await page.getByText('The application changed or the reviewed scope differs. Reload and review the template again.',{exact:true}).first().waitFor()
    assert.equal(await page.evaluate(async()=> (await window.__templateFixture.readEditor())[0]),edited)
    assert.equal(await page.evaluate(()=>window.__templateFixture.deployments.length),0);await capture(page,id)
  })
  await run('permission-denied','new',theme,390,async(page,id)=>{
    await settled(page,page.getByRole('heading',{name:'Deployment access required',exact:true}));assert.equal(await page.getByRole('button',{name:'Edit TOML',exact:true}).count(),0);await capture(page,id)
  },'denied')
}
await browser.close()
assert.deepEqual(await hash(),initialHashes,'Product source changed during review')
await writeFile(`${output}/results.json`,JSON.stringify({artificial:true,sourceHashes:initialHashes,cases,captures},null,2)+'\n')
console.log(JSON.stringify({cases:cases.length,failed:cases.filter(c=>c.status==='failed').map(c=>({id:c.id,error:c.error})),captures:captures.length,output},null,2))
if(cases.some(c=>c.status==='failed'))process.exitCode=1
