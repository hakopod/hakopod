package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hakopod/hakopod/internal/spec"
)

func readEnvironmentFiles(configPath string, data []byte) (map[string]string, error) {
	names, err := spec.EnvironmentFileNames(data)
	if err != nil || len(names) == 0 {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(configPath))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files, total := map[string]string{}, 0
	for _, name := range names {
		file, err := root.Open(filepath.FromSlash(name))
		if err != nil {
			return nil, fmt.Errorf("env_file: cannot read %s inside the configuration directory", name)
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			file.Close()
			return nil, fmt.Errorf("env_file: %s must be a regular file", name)
		}
		body, err := io.ReadAll(io.LimitReader(file, int64(spec.MaxEnvironmentFileBytes-total+1)))
		file.Close()
		total += len(body)
		if err != nil || total > spec.MaxEnvironmentFileBytes {
			return nil, fmt.Errorf("env_file: files must total at most 128 KiB")
		}
		files[name] = string(body)
	}
	return files, nil
}

func validateLocalConfiguration(file string, data []byte) (spec.Application, map[string]string, error) {
	files, err := readEnvironmentFiles(file, data)
	if err != nil {
		return spec.Application{}, nil, err
	}
	if len(files) == 0 {
		app, err := spec.Parse(data)
		return app, nil, err
	}
	result, err := spec.ImportEnvironmentFiles(data, files, func(string, string) string { return "envfile-validation" })
	return result.Spec, files, err
}
