package main

import "testing"

func TestDefaultString(t *testing.T) {
	if got := defaultString("configured", "fallback"); got != "configured" {
		t.Fatalf("defaultString 覆盖了已有值: %q", got)
	}
	if got := defaultString("", "fallback"); got != "fallback" {
		t.Fatalf("defaultString 默认值 = %q", got)
	}
}
