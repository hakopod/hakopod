import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { editionFetch } from '../lib/client-edition'
import { message } from '../lib/api'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Note } from '../components/shared'

type Item = { id: string; category: string; path: string; bytes: number }
type Review = { id: string; expires_at: string; inventory: { filesystem: { capacity_bytes: number; available_bytes: number }; items: Item[]; protected: string[]; planned_bytes: number } }
const size=(bytes:number)=>`${(bytes/1024**2).toLocaleString(undefined,{maximumFractionDigits:1})} MiB`

export const Route = createFileRoute('/infrastructure/cleanup')({ component: Cleanup })
function Cleanup(){
 const [review,setReview]=useState<Review|null>(null),[confirmation,setConfirmation]=useState(''),[result,setResult]=useState<Record<string,unknown>|null>(null),[error,setError]=useState(''),[busy,setBusy]=useState(false)
 async function request(path:string,body?:unknown){const response=await editionFetch('/api/installation/cleanup/'+path,{method:'POST',headers:{'Content-Type':'application/json',...(body?{'Idempotency-Key':crypto.randomUUID()}:{})},body:body?JSON.stringify(body):'{}'});const value=await response.json();if(!response.ok)throw new Error(value?.error?.message||'Cleanup request failed.');return value}
 async function preview(){setBusy(true);setError('');try{setReview(await request('review'));setResult(null)}catch(e){setError(message(e))}finally{setBusy(false)}}
 async function execute(){if(!review)return;setBusy(true);setError('');try{setResult(await request('execute',{review_id:review.id,confirmation}));setReview(null);setConfirmation('')}catch(e){setError(message(e))}finally{setBusy(false)}}
 return <FormPage title="Safe server cleanup" description="Review exact installer-owned temporary files before removing them." breadcrumbs={[]}>
  <FormSection title="Disk inventory">
   <Note>Cleanup never includes database volumes, application volumes, backups, K3s state, secrets, installed releases or rollback material. RAM pressure is separate from disk usage.</Note>
   {!review&&<Button variant="primary" disabled={busy} onClick={()=>void preview()}>{busy?'Checking…':'Preview removable files'}</Button>}
   {review&&<><dl className="service-definition-list"><div><dt>Disk capacity</dt><dd>{size(review.inventory.filesystem.available_bytes)} available of {size(review.inventory.filesystem.capacity_bytes)}</dd></div><div><dt>Memory pressure</dt><dd>Not measured by cleanup. Inspect live node memory separately.</dd></div><div><dt>Planned removal</dt><dd>{size(review.inventory.planned_bytes)} across {review.inventory.items.length} files</dd></div></dl>
    <div className="table-scroll" tabIndex={0}><table><thead><tr><th>Category</th><th>Exact file</th><th>Allocated size</th></tr></thead><tbody>{review.inventory.items.map(item=><tr key={item.id}><td>{item.category}</td><td><code className="break-all">{item.path}</code></td><td>{size(item.bytes)}</td></tr>)}</tbody></table></div>
    {!review.inventory.items.length?<Note>No reviewed temporary files are removable.</Note>:<label>Type remove reviewed files to confirm<Input value={confirmation} onChange={e=>setConfirmation(e.target.value)} autoComplete="off"/></label>}
   </>}
   {result&&<Note>Cleanup completed. The receipt records removed, skipped and reclaimed bytes.</Note>}{error&&<FormError>{error}</FormError>}
  </FormSection>
  <div className="form-footer"><Button asChild><Link to="/infrastructure" search={{tab:'nodes'}}>Back</Link></Button>{review&&<Button variant="primary" disabled={busy||confirmation!=='remove reviewed files'||!review.inventory.items.length} onClick={()=>void execute()}>{busy?'Removing…':'Remove reviewed files'}</Button>}</div>
 </FormPage>
}
