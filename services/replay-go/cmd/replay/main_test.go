package main

import "testing"

func TestValidateSlotRange(t *testing.T) {
	cases := []struct {
		start int64
		end   int64
		ok    bool
	}{
		{start: 1, end: 1, ok: true},
		{start: 1, end: 100, ok: true},
		{start: 0, end: 1, ok: false},
		{start: 1, end: 0, ok: false},
		{start: 10, end: 9, ok: false},
	}

	for _, tc := range cases {
		err := validateSlotRange(tc.start, tc.end)
		if tc.ok && err != nil {
			t.Fatalf("expected ok for start=%d end=%d, got %v", tc.start, tc.end, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("expected error for start=%d end=%d", tc.start, tc.end)
		}
	}
}

func TestRequiredPGDSN(t *testing.T) {
	t.Setenv("HELIOS_PG_DSN", "")
	if _, err := requiredPGDSN(); err == nil {
		t.Fatalf("expected error when HELIOS_PG_DSN is empty")
	}

	t.Setenv("HELIOS_PG_DSN", "postgres://user:pass@host/db?sslmode=require")
	v, err := requiredPGDSN()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v == "" {
		t.Fatalf("expected non-empty dsn")
	}
}
