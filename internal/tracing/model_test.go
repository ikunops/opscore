package tracing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestTraceIDIsNotExecutionID (P-2 / M-2) proves the TraceID is randomly minted
// and is NOT derived from, nor equal to a hash of, any existing id such as an
// execution ref (R20-6, R20-10 advisory resolution).
func TestTraceIDIsNotExecutionID(t *testing.T) {
	_, span := StartSpan(context.Background(), "op")
	span.WithRef("execution", "exec-xyz-789")
	if span.TraceID == "" {
		t.Fatal("trace id must not be empty")
	}
	if span.TraceID == "exec-xyz-789" {
		t.Fatal("trace id must not equal an execution ref (R20-6)")
	}
	h := sha256.Sum256([]byte("exec-xyz-789"))
	if span.TraceID == hex.EncodeToString(h[:]) {
		t.Fatal("trace id must not be a hash of an execution ref (R20-6/R20-10)")
	}
	// Two independent traces must have distinct ids.
	_, span2 := StartSpan(context.Background(), "op")
	if span.TraceID == span2.TraceID {
		t.Fatal("independent traces must differ (R20-6)")
	}
}

// TestRefsAreNotIdentity (P-3 / M-3) proves a zero-ref span is valid and that
// attaching a ref never alters the span's independent identity (R20-6, R20-10).
func TestRefsAreNotIdentity(t *testing.T) {
	s := Span{TraceID: "t1", SpanID: "s1", Operation: "op"}
	if _, ok := s.Ref("execution"); ok {
		t.Fatal("empty refs must not yield a ref")
	}
	s2 := Span{TraceID: "t1", SpanID: "s1", Operation: "op"}
	s2.WithRef("execution", "exec-1")
	if s2.TraceID != "t1" || s2.SpanID != "s1" {
		t.Fatal("attaching a ref must not alter trace/span identity (R20-6)")
	}
}

// TestNoSamplingPolicy (P-4 / M-4) proves no sampler surface exists in the
// public source: eligible spans are captured wholesale (R20-9).
func TestNoSamplingPolicy(t *testing.T) {
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"Sampler", "Sample", "SamplingRatio", "SampleRate", "SampleFunc"}
	for _, f := range matches {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, tok := range forbidden {
			if strings.Contains(string(src), tok) {
				t.Fatalf("%s contains forbidden sampling token %q (R20-9)", f, tok)
			}
		}
	}
}

// TestSpanHasExactlySevenFields (§3 reflection guard) proves no SpanKind /
// Attributes / Status / Events field was added (M-3). The Span is frozen at
// exactly seven plain-data fields.
func TestSpanHasExactlySevenFields(t *testing.T) {
	want := map[string]bool{
		"TraceID": true, "SpanID": true, "ParentSpanID": true,
		"Start": true, "Ended": true, "Operation": true, "Refs": true,
	}
	typ := reflect.TypeOf(Span{})
	if typ.NumField() != 7 {
		t.Fatalf("Span has %d fields, want exactly 7 (R20-6: no SpanKind/Attributes/Status/Events)", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		if !want[typ.Field(i).Name] {
			t.Errorf("Span has unexpected field %q", typ.Field(i).Name)
		}
	}
}
