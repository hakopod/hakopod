package api

import (
 "context"
 "net/http/httptest"
 "strings"
 "testing"
 "time"
 "github.com/hakopod/hakopod/internal/actions"
 "github.com/hakopod/hakopod/internal/cluster"
 "github.com/hakopod/hakopod/internal/spec"
 "github.com/hakopod/hakopod/internal/store"
)
type workflowRuntimeFixture struct { *actionsFake; reads int }
func(f *workflowRuntimeFixture) ActionsWorkflowOutput(context.Context,cluster.Target,string,string)(actions.Output,error){
 f.reads++;return actions.Output{Available:true,Lines:[]actions.LogLine{{Number:1,Text:"scoped workflow ***"}}},nil
}
func TestWorkflowHistoryAndLogsScope(t *testing.T){
 db:=actionsDatabase(t);ctx:=context.Background()
 token,err:=db.Bootstrap(ctx,"workflow-fixture");if err!=nil{t.Fatal(err)}
 owner,_:=db.Authenticate(ctx,token)
 normal,_:=spec.Parse([]byte("name='workflow-fixture'\n[services.runner]\nimage='nginx:alpine'"))
 dep,err:=db.Accept(ctx,owner,"demo","development",normal,0,"workflow-fixture-deploy");if err!=nil{t.Fatal(err)}
 app:=store.Application{ID:dep.ApplicationID,Project:"demo",Environment:"development",Name:normal.Name}
 config,err:=spec.Normalize(spec.Application{Name:normal.Name,Services:map[string]spec.Service{"runner":{Actions:&spec.Actions{Repository:"team/repo",Credential:"token"}}}});if err!=nil{t.Fatal(err)}
 if err=db.SyncActions(ctx,app,config,1);err!=nil{t.Fatal(err)}
 pools,_:=db.ActionsPools(ctx,app.ID)
 slot,err:=db.NewActionsSlot(ctx,pools[0]);if err!=nil{t.Fatal(err)}
 slot.RunnerID=42
 if err=db.UpdateActionsSlot(ctx,slot.ID,42,"busy");err!=nil{t.Fatal(err)}
 observation:=actions.Observation{Repository:"team/repo",RunID:9,Attempt:1,JobKey:"build"}
 if err=db.RecordActionsJob(ctx,slot,observation);err!=nil{t.Fatal(err)}
 // A second hook cannot replace an already recorded job identity.
 forged:=observation;forged.RunID=99
 if err=db.RecordActionsJob(ctx,slot,forged);err!=nil{t.Fatal(err)}
 jobs,_:=db.ActionsJobs(ctx,app.ID,"runner")
 if len(jobs)!=1||jobs[0].Observation.RunID!=9{t.Fatal("history identity changed",jobs)}
 // Metadata can survive registration cleanup; it is isolated to this pool.
 if other,_:=db.ActionsJobs(ctx,app.ID,"other");len(other)!=0{t.Fatal("pool isolation")}
 _,reader,err:=db.CreateKey(ctx,owner,store.KeyInput{Name:"metadata-only",Project:"demo",Environment:"development",Permissions:[]string{"deployments:read"},ExpiresAt:time.Now().Add(time.Hour)});if err!=nil{t.Fatal(err)}
 _,wrong,err:=db.CreateKey(ctx,owner,store.KeyInput{Name:"other-env",Project:"demo",Environment:"production",Permissions:[]string{"logs:read"},ExpiresAt:time.Now().Add(time.Hour)});if err!=nil{t.Fatal(err)}
 runtime:=&workflowRuntimeFixture{actionsFake:&actionsFake{}}
 server:=(&Server{Store:db,actionsTestRuntime:runtime}).Handler()
 base:="/api/v1/applications/"+app.ID+"/actions/runner/jobs/"
 for _,test:=range []struct{path,key string;want int}{{base+slot.ID+"/logs",reader,403},{base+slot.ID+"/logs",wrong,403},{base+strings.Repeat("a",32)+"/logs",token,404},{base+slot.ID+"/logs",token,200}}{
  req:=httptest.NewRequest("GET",test.path,nil);req.Header.Set("Authorization","Bearer "+test.key)
  out:=httptest.NewRecorder();server.ServeHTTP(out,req)
  if out.Code!=test.want{t.Fatalf("got %d want %d: %s",out.Code,test.want,out.Body.String())}
  if out.Code!=200 && strings.Contains(out.Body.String(),"scoped workflow"){t.Fatal("log leaked")}
 }
 if runtime.reads!=1{t.Fatal("unauthorized request reached runner",runtime.reads)}
 if err=db.DeleteActionsSlot(ctx,slot.ID);err!=nil{t.Fatal(err)}
 if jobs,_=db.ActionsJobs(ctx,app.ID,"runner");len(jobs)!=1{t.Fatal("cleanup removed history")}
 if _,err=db.Pool.Exec(ctx,`UPDATE actions_jobs SET created_at=now()-interval '31 days' WHERE slot_id=$1`,slot.ID);err!=nil{t.Fatal(err)}
 if jobs,_=db.ActionsJobs(ctx,app.ID,"runner");len(jobs)!=0{t.Fatal("expired metadata visible")}
}
