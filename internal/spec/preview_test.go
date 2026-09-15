package spec

import "testing"

func TestPreviewIsolationAndBudget(t *testing.T) {
	base, _ := Normalize(Application{Name: "preview", Services: map[string]Service{"web": {Image: "nginx:alpine"}}})
	for _, change := range []func(*Application){
		func(a *Application) { a.Domains = map[string]string{"web": "production.example.com"} },
		func(a *Application) { s := a.Services["web"]; s.Replicas = 2; a.Services["web"] = s },
		func(a *Application) { s := a.Services["web"]; s.AWSIdentity = "production"; a.Services["web"] = s },
		func(a *Application) { a.Networks = map[string]Network{"shared": {VirtualNetwork: "production"}} },
		func(a *Application) {
			s := a.Services["web"]
			s.Secrets = map[string]SecretRef{"PASSWORD": {Provider: "vault", Ref: "production"}}
			a.Services["web"] = s
		},
		func(a *Application) { s := a.Services["web"]; s.Volume = &Volume{SizeGiB: 6}; a.Services["web"] = s },
	} {
		a, _ := Normalize(base)
		change(&a)
		if ValidatePreview(a) == nil {
			t.Fatal("unsafe preview allowed", a)
		}
	}
	if err := ValidatePreview(base); err != nil {
		t.Fatal(err)
	}
}
