package store

import (
	"context"
	"sync"
	"testing"
)

func TestEmbeddingApplicationLimitSerializesDifferentNames(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	db.ApplicationLimit = func(context.Context, string, string) (int, error) { return 1, nil }
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			app := emptyTestSpec()
			app.Name = name
			_, err := db.Accept(ctx, p, "demo", "development", app, 0, "limited-"+name)
			errors <- err
		}(name)
	}
	wg.Wait()
	close(errors)
	accepted := 0
	for err := range errors {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d concurrent first applications", accepted)
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM applications").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
