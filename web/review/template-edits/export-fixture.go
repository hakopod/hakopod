//go:build ignore

// Run only in the reserved VM checkout. All identities and scope are artificial.
package main

import (
	"encoding/json"
	"os"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
)

func must[T any](value T, err error) T { if err != nil { panic(err) }; return value }
func clone(value spec.Application) spec.Application { return must(spec.Parse(must(toml.Marshal(value)))) }

func main() {
	var template spec.Template
	for _, item := range spec.Templates() { if item.ID == "outpost" { template = item } }
	if template.ID == "" { panic("Outpost catalog entry missing") }
	options := spec.TemplateOptions{Name: "fixture-outpost", StorageGiB: 5, Architecture: "arm64", Values: map[string]string{"database-mode":"bundled", "redis-mode":"bundled", "broker-mode":"bundled"}}
	baseline := must(spec.PlanTemplate("outpost", options))
	prior := must(spec.PlanTemplate("redis", spec.TemplateOptions{Name: options.Name, StorageGiB:5, Architecture:"arm64"}))
	prior = must(spec.NameTemplateService(prior, "previous-cache"))
	external := options
	external.Values = map[string]string{"database-mode":"external", "redis-mode":"external", "broker-mode":"external", "redis-host":"redis.example.invalid"}
	edited := must(spec.PlanTemplate("outpost", external))
	delete(edited.Services, "log")
	main := edited.Services["main"]
	main.Secrets["CUSTOM_API_KEY"] = spec.SecretRef{Ref:"custom-api-key"}
	edited.Services["main"] = main
	edited = clone(edited)
	plans := map[string]any{}
	for _, target := range []string{"new","existing"} {
		var before *spec.Application
		var revision int64
		id := ""
		next := clone(baseline)
		if target == "existing" { before=&prior; revision=7; id="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"; next=must(spec.AddServices(prior,next)) }
		for _, phase := range []string{"baseline","edited"} {
			app := next
			if phase=="edited" { app=edited }
			canonical := string(must(toml.Marshal(app)))
			configuration := map[string]any{"name":options.Name,"project":"fixture-review","environment":"development","storage_gib":5,"architecture":"arm64","public":false,"values":options.Values}
			if id!="" { configuration["application_id"]=id;configuration["expected_revision"]=revision }
			required:=spec.TemplateSecretNames(baseline)
			warnings:=[]string{"Artificial review fixture. No deployment was submitted."}
			if phase=="edited" { configuration["toml"]=canonical;required=spec.LocalSecretNames(app);warnings=append(warnings,"This edited application uses the configuration shown in the review. Catalog verification applies to the original template.") }
			plans[target+"-"+phase]=map[string]any{"application_id":id,"expected_revision":revision,"spec":app,"toml":canonical,"configuration":configuration,"changes":spec.Diff(before,app),"warnings":warnings,"resource_profiles":spec.Profiles,"required_secrets":required,"model_source":""}
		}
	}
	if err:=json.NewEncoder(os.Stdout).Encode(map[string]any{"artificial":true,"generator":"spec.PlanTemplate, AddServices, Parse, Diff, LocalSecretNames","items":[]spec.Template{template},"plans":plans,"previous":prior});err!=nil{panic(err)}
}
