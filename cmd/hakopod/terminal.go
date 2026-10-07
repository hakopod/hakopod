package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

func terminal(ctx context.Context, c *client, app, service, pod, container, commandJSON string) error {
	boundedClient := *c
	boundedHTTP := *c.http
	if boundedHTTP.Timeout <= 0 || boundedHTTP.Timeout > 30*time.Second {
		boundedHTTP.Timeout = 30 * time.Second
	}
	boundedClient.http = &boundedHTTP
	c = &boundedClient
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
	if len(session.ID) != 32 || strings.Trim(session.ID, "0123456789abcdef") != "" {
		return fmt.Errorf("API returned invalid terminal session")
	}
	base += "/" + url.PathEscape(session.ID)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = c.request(cleanup, "DELETE", base, nil, "", nil)
	}()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if isTTY {
		old, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}
		defer term.Restore(fd, old)
	}
	inputError := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		select {
		case <-ready:
		case <-ctx.Done():
			return
		}
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
			select {
			case <-ready:
			case <-ctx.Done():
				return
			}
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
	err := pollTerminalOutputReady(ctx, c, base, os.Stdout, ready)
	select {
	case inputErr := <-inputError:
		return inputErr
	default:
	}
	return err
}
func pollTerminalOutput(ctx context.Context, c *client, base string, output io.Writer) error {
	return pollTerminalOutputReady(ctx, c, base, output, nil)
}
func pollTerminalOutputReady(ctx context.Context, c *client, base string, output io.Writer, ready chan struct{}) error {
	httpClient := *c.http
	if httpClient.Timeout <= 0 || httpClient.Timeout > 30*time.Second {
		httpClient.Timeout = 30 * time.Second
	}
	cursor := int64(0)
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", c.url+"/api/v1"+base+"/poll?cursor="+strconv.FormatInt(cursor, 10), nil)
		if err != nil {
			return err
		}
		c.authorizeRequest(req)
		res, err := httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("Terminal output request failed: %w", err)
		}
		if res.StatusCode != 200 {
			err = responseError(res)
			res.Body.Close()
			return err
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
		res.Body.Close()
		if err != nil {
			return fmt.Errorf("Terminal output read failed: %w", err)
		}
		if len(data) > 2<<20 {
			return fmt.Errorf("Terminal response exceeds the output limit")
		}
		var batch struct {
			Frames []struct {
				Cursor int64           `json:"cursor"`
				Data   json.RawMessage `json:"data"`
			} `json:"frames"`
			Next      string `json:"next_cursor"`
			Truncated bool   `json:"truncated"`
			Done      bool   `json:"done"`
		}
		if json.Unmarshal(data, &batch) != nil || len(batch.Frames) > 256 {
			return fmt.Errorf("Terminal response is invalid")
		}
		next, err := strconv.ParseInt(batch.Next, 10, 64)
		if err != nil || next < cursor || batch.Next != strconv.FormatInt(next, 10) {
			return fmt.Errorf("Terminal cursor is invalid")
		}
		if batch.Truncated {
			return fmt.Errorf("Terminal output was truncated. Close this terminal and start a new terminal")
		}
		if ready != nil {
			close(ready)
			ready = nil
		}
		for _, frame := range batch.Frames {
			if frame.Cursor != cursor+1 || frame.Cursor > next {
				return fmt.Errorf("Terminal frame cursor is invalid")
			}
			cursor = frame.Cursor
			var event struct {
				Type    string `json:"type"`
				Data    string `json:"data"`
				Code    *int   `json:"code"`
				Message string `json:"message"`
			}
			if json.Unmarshal(frame.Data, &event) != nil {
				return fmt.Errorf("Terminal frame is invalid")
			}
			switch event.Type {
			case "output":
				decoded, err := base64.StdEncoding.DecodeString(event.Data)
				if err != nil || len(decoded) > 4096 {
					return fmt.Errorf("Terminal output is invalid")
				}
				if _, err = output.Write(decoded); err != nil {
					return err
				}
			case "exit":
				if event.Code == nil {
					return fmt.Errorf("Terminal exit frame is invalid")
				}
				if *event.Code != 0 {
					code := *event.Code
					if code < 1 || code > 255 {
						return &exitError{1, "Terminal transport closed. The process outcome is unknown"}
					}
					return &exitError{code, "Terminal process failed"}
				}
				return nil
			default:
				return fmt.Errorf("Terminal frame type is invalid")
			}
		}
		if cursor != next {
			return fmt.Errorf("Terminal response omitted output frames")
		}
		if batch.Done {
			return fmt.Errorf("Terminal closed without an exit frame")
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
