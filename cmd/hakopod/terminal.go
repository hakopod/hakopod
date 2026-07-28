package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

func terminal(ctx context.Context, c *client, app, service, pod, container, commandJSON string) error {
	if service == "" || pod == "" {
		return &exitError{2, "terminal requires --service and --pod; use the dashboard runtime inspector to select a running pod"}
	}
	command := []string{"/bin/sh"}
	if commandJSON != "" {
		if err := json.Unmarshal([]byte(commandJSON), &command); err != nil || len(command) == 0 {
			return &exitError{2, "--command-json must be a nonempty JSON array of executable and arguments"}
		}
	}
	cols, rows := 100, 30
	fd := int(os.Stdin.Fd())
	isTTY := term.IsTerminal(fd)
	if isTTY {
		if w, h, err := term.GetSize(fd); err == nil {
			cols = max(20, min(w, 400))
			rows = max(5, min(h, 200))
		}
	}
	base := "/applications/" + url.PathEscape(app) + "/services/" + url.PathEscape(service) + "/terminal"
	var session struct {
		ID string `json:"id"`
	}
	if err := c.request(ctx, "POST", base, map[string]any{"pod": pod, "container": container, "command": command, "cols": cols, "rows": rows}, "", &session); err != nil {
		return err
	}
	if len(session.ID) != 32 {
		return fmt.Errorf("API returned invalid terminal session")
	}
	base += "/" + url.PathEscape(session.ID)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = c.request(cleanup, "DELETE", base, nil, "", nil)
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.url+"/api/v1"+base+"/output", nil)
	req.Header.Set("Authorization", "Bearer "+c.key)
	streamClient := *c.http
	streamClient.Timeout = 11 * time.Minute
	res, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("terminal connection failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return responseError(res)
	}
	if isTTY {
		old, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}
		defer term.Restore(fd, old)
	}
	inputError := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if sendErr := c.request(ctx, "POST", base+"/input", map[string]string{"data": base64.StdEncoding.EncodeToString(buf[:n])}, "", nil); sendErr != nil {
					inputError <- sendErr
					cancel()
					return
				}
			}
			if err != nil {
				if err == io.EOF {
					_ = c.request(ctx, "POST", base+"/input", map[string]string{"data": "BA=="}, "", nil)
				}
				return
			}
		}
	}()
	if isTTY {
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					w, h, err := term.GetSize(fd)
					if err != nil {
						continue
					}
					w = max(20, min(w, 400))
					h = max(5, min(h, 200))
					if w != cols || h != rows {
						if c.request(ctx, "POST", base+"/input", map[string]int{"cols": w, "rows": h}, "", nil) != nil {
							return
						}
						cols, rows = w, h
					}
				}
			}
		}()
	}
	err = readTerminalOutput(res.Body, os.Stdout)
	select {
	case inputErr := <-inputError:
		return inputErr
	default:
	}
	return err
}
func readTerminalOutput(input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 16<<10)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Type    string `json:"type"`
			Data    string `json:"data"`
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
			return fmt.Errorf("invalid terminal frame")
		}
		switch event.Type {
		case "output":
			data, err := base64.StdEncoding.DecodeString(event.Data)
			if err != nil || len(data) > 4096 {
				return fmt.Errorf("invalid terminal output")
			}
			if _, err = output.Write(data); err != nil {
				return err
			}
		case "exit":
			if event.Code != 0 {
				code := event.Code
				if code < 1 || code > 255 {
					code = 1
				}
				return &exitError{code, event.Message}
			}
			return nil
		default:
			return fmt.Errorf("unknown terminal frame")
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("terminal output ended: %w", err)
	}
	return fmt.Errorf("terminal connection closed")
}
