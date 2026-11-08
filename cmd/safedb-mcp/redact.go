package main

import (
	"os"
	"strings"
	"unicode/utf8"
)

// Second layer behind the database grants: masks result columns by name, in case a grant
// exposes a PII column by mistake. It only sees the column's name, so an alias
// (SELECT email AS x) or an expression gets past it. The grants are the real protection.

const (
	redacted     = "[redacted]"
	phoneVisible = 4
)

var customRedactColumn = parseRedactEnv()

func parseRedactEnv() map[string]bool {
	m := make(map[string]bool)
	val := os.Getenv("REDACT_COLUMNS")
	if val == "" {
		return m
	}
	for _, col := range strings.Split(val, ",") {
		col = strings.TrimSpace(strings.ToLower(col))
		if col != "" {
			m[col] = true
		}
	}
	return m
}

func redactorFor(column string) func(string) string {
	name := strings.ToLower(column)

	if customRedactColumn[name] {
		return func(string) string { return redacted }
	}

	if strings.HasPrefix(name, "is_") || strings.HasPrefix(name, "has_") ||
		strings.HasSuffix(name, "_id") || strings.HasSuffix(name, "_count") ||
		strings.HasSuffix(name, "_at") || strings.HasSuffix(name, "_date") ||
		strings.HasSuffix(name, "_type") || strings.HasSuffix(name, "_status") {
		return nil
	}

	if name == "ip_address" || name == "mac_address" {
		return nil
	}

	switch {
	case name == "email" || strings.HasSuffix(name, "_email"):
		return maskEmail
	case name == "phone" || strings.HasSuffix(name, "_phone") ||
		name == "phone_number" || strings.HasSuffix(name, "_phone_number"):
		return maskPhone
	case name == "address" || strings.HasSuffix(name, "_address") ||
		name == "street" || strings.HasSuffix(name, "_street"):
		return func(string) string { return redacted }
	}

	return nil
}

func maskEmail(s string) string {
	at := strings.LastIndexByte(s, '@')
	if at < 1 {
		return redacted
	}
	_, first := utf8.DecodeRuneInString(s)
	return s[:first] + "***" + s[at:]
}

func maskPhone(s string) string {
	r := []rune(s)
	hidden := max(len(r)-phoneVisible, 0)
	return strings.Repeat("*", hidden) + string(r[hidden:])
}
