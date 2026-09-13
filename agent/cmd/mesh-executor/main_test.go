package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVersionDoesNotRequireRuntime(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"version"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) == "" {
		t.Fatal("version output was empty")
	}
}

func TestReconcileRejectsUnboundedAndUnknownInputBeforeRuntime(t *testing.T) {
	requests := []string{strings.Repeat("x", maxRequestBytes+1), `{"unexpected":true}`,
		`{"workload":{},"inputPath":"","another":true}`}
	for _, request := range requests {
		if err := run(context.Background(), []string{"reconcile"}, strings.NewReader(request), &bytes.Buffer{}); err == nil {
			t.Fatal("invalid executor input accepted")
		}
	}
}
