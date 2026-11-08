package main

import "testing"

func TestRedactorFor(t *testing.T) {
	tests := []struct {
		column, value, want string
	}{
		{"email", "MARY.SMITH@sakilacustomer.org", "M***@sakilacustomer.org"},
		{"Contact_Email", "mary@example.com", "m***@example.com"},
		{"email", "M***@sakilacustomer.org", "M***@sakilacustomer.org"}, // already masked by customer_safe
		{"email", "not an email", redacted},
		{"phone", "6172235589", "******5589"},
		{"work_phone", "*******4290", "*******4290"}, // already masked by customer_safe
		{"address", "47 MySakila Drive", redacted},
		{"first_name", "MARY", "MARY"},
		{"x", "MARY.SMITH@sakilacustomer.org", "MARY.SMITH@sakilacustomer.org"}, // an alias gets past this layer
	}
	for _, tt := range tests {
		got := tt.value
		if redact := redactorFor(tt.column); redact != nil {
			got = redact(tt.value)
		}
		if got != tt.want {
			t.Errorf("column %q, value %q: got %q, want %q", tt.column, tt.value, got, tt.want)
		}
	}
}
