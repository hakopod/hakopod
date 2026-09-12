package api

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

func projectMetadata(id string, name, description *string) (string, string, error) {
	display, detail := id, ""
	if name != nil {
		display = strings.TrimSpace(*name)
	}
	if description != nil {
		detail = strings.TrimSpace(*description)
	}
	if display == "" || !utf8.ValidString(display) || utf8.RuneCountInString(display) > 80 || strings.ContainsFunc(display, unicode.IsControl) {
		return "", "", errors.New("Project name must have 1–80 characters without control characters.")
	}
	if !utf8.ValidString(detail) || utf8.RuneCountInString(detail) > 1000 || strings.ContainsFunc(detail, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) {
		return "", "", errors.New("Description must have at most 1000 characters without control characters.")
	}
	return display, detail, nil
}
