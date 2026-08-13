package main

import "testing"

func TestVersion(t *testing.T) {
	if version != "0.1.0" {
		t.Fatalf("version = %q, want %q", version, "0.1.0")
	}
}
