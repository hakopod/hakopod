package spec

import (
	"net/url"
	"testing"
)

func TestPrivateBindingsEscapeCredentialsAndRespectIsolation(t *testing.T) {
	app := Application{Name: "binding", Services: map[string]Service{"db": {Image: "postgres:17", Port: 5432}, "web": {Image: "example/app:1", Bindings: map[string]Binding{"DATABASE_URL": {Service: "db", Protocol: "postgres", Username: "app", Database: "app", Password: &SecretRef{Ref: "password"}}}}}}
	next, err := Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	b := next.Services["web"].Bindings["DATABASE_URL"]
	value := BindingURL(b, next.Services["db"], []byte("p@ss:/?#word"))
	u, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if password != "p@ss:/?#word" || u.Host != "db:5432" || u.Path != "/app" {
		t.Fatal("incorrect credential escaping")
	}
	db := app.Services["db"]
	db.NetworkAccess = &NetworkAccess{From: []string{}}
	app.Services["db"] = db
	if _, err := Normalize(app); err == nil {
		t.Fatal("binding bypassed network isolation")
	}
}

func TestNamedHTTPEndpoints(t *testing.T) {
	app := Application{Name: "endpoints", Services: map[string]Service{"web": {Image: "example/app:1", Port: 8080, Public: true, Ports: []Port{{Name: "console", Port: 9000, TargetPort: 9001}}, HTTP: map[string]HTTPEndpoint{"console": {Port: 9000, Domain: "console.example.com"}}}}, Domains: map[string]string{"console.example.com": "web"}}
	if _, err := Normalize(app); err != nil {
		t.Fatal(err)
	}
	app.Services["web-console"] = Service{Image: "example/app:1"}
	if _, err := Normalize(app); err == nil {
		t.Fatal("colliding generated hostname accepted")
	}
	delete(app.Services, "web-console")
	app.Domains = nil
	if _, err := Normalize(app); err == nil {
		t.Fatal("unassigned domain accepted")
	}
}
