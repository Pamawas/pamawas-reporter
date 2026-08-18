package otel

import (
	"context"
	"testing"
)

func TestInitTracerDisabled(t *testing.T) {
	shutdown, err := InitTracer(Config{ServiceName: "test", Enabled: false})
	if err != nil {
		t.Fatalf("InitTracer() error = %v", err)
	}
	if shutdown == nil {
		t.Fatal("InitTracer() returned nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if Tracer("unit-test") == nil {
		t.Fatal("Tracer returned nil")
	}
}

func TestInitTracerWithoutEndpointIsDisabled(t *testing.T) {
	shutdown, err := InitTracer(Config{ServiceName: "test", Enabled: true})
	if err != nil {
		t.Fatalf("InitTracer() error = %v", err)
	}
	if shutdown == nil {
		t.Fatal("InitTracer() returned nil shutdown")
	}
}
