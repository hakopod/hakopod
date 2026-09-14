package api

import (
	"encoding/csv"
	"sort"
	"strings"
)

func sortedBuildArguments(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for key := range values {
		names = append(names, key)
	}
	sort.Strings(names)
	for i, key := range names {
		names[i] = key + "=" + values[key]
	}
	return names
}
func workflowBuildArguments(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	var result strings.Builder
	result.WriteString("          build-args: |\n")
	for _, argument := range sortedBuildArguments(values) {
		var line strings.Builder
		writer := csv.NewWriter(&line)
		_ = writer.Write([]string{argument})
		writer.Flush()
		result.WriteString("            " + line.String())
	}
	return result.String()
}
func shellBuildArguments(values map[string]string, flag string) string {
	var result strings.Builder
	for _, argument := range sortedBuildArguments(values) {
		result.WriteString(" " + flag + " '" + strings.ReplaceAll(argument, "'", "'\"'\"'") + "'")
	}
	return result.String()
}
