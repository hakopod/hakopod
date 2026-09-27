package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/store"
)

type databaseImportFlags struct{ Destination, Engine, Version, Captured string }

func databaseImportCommand(ctx context.Context, c *client, args []string, file, idem, name string, flags databaseImportFlags) error {
	if name == "" {
		return fmt.Errorf("provide --name with the reviewed Docker source name")
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() || stat.Size() < 16 || stat.Size() > backup.ImportMaxBytes {
		return fmt.Errorf("choose a regular archive file up to 2 GiB")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, io.LimitReader(f, backup.ImportMaxBytes+1)); err != nil {
		return err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if args[0] == "import-plan" {
		if len(args) != 1 {
			return fmt.Errorf("import-plan takes archive flags without a database ID")
		}
		captured, err := time.Parse(time.RFC3339, flags.Captured)
		if err != nil {
			return fmt.Errorf("--captured-at must be RFC3339 with the original capture time and timezone")
		}
		input := backup.ImportSpec{DestinationID: flags.Destination, SourceName: name, Engine: flags.Engine, SourceVersion: flags.Version, CapturedAt: captured, Bytes: stat.Size(), SHA256: digest}
		if err = input.Validate(time.Now()); err != nil {
			return err
		}
		if idem == "" {
			idem = store.NewID()
		}
		var out backup.Import
		if err = c.request(ctx, "POST", "/backup-imports", input, idem, &out); err != nil {
			return err
		}
		return printJSON(out)
	}
	if len(args) != 2 || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(args[1]) {
		return fmt.Errorf("import requires the ID returned by import-plan")
	}
	var review backup.Import
	if err = c.request(ctx, "GET", "/backup-imports/"+args[1], nil, "", &review); err != nil {
		return err
	}
	if review.Spec.SourceName != name || review.Spec.Bytes != stat.Size() || review.Spec.SHA256 != digest {
		return fmt.Errorf("archive or source name differs from the import review")
	}
	if review.Status != "completed" && !time.Now().Before(review.ExpiresAt) {
		return fmt.Errorf("import review expired; prepare a new review")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "PUT", c.url+"/api/v1/backup-imports/"+review.ID+"/archive", f)
	if err != nil {
		return err
	}
	request.ContentLength = stat.Size()
	request.Header.Set("Content-Type", "application/octet-stream")
	c.authorizeRequest(request)
	transport := *c.http
	transport.Timeout = 15 * time.Minute
	response, err := transport.Do(request)
	if err != nil {
		return fmt.Errorf("archive upload interrupted; retry this import ID to check its durable result")
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return responseError(response)
	}
	var out backup.Artifact
	if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&out); err != nil {
		return err
	}
	return printJSON(out)
}
