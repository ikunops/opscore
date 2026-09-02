package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// R149=B — P32-I15 crash consistency: a consumed sequence is never re-issued
// ---------------------------------------------------------------------------

// --- T-m3 ------------------------------------------------------------------
// R149's counterexample: Write+Sync succeed, then Close reports an error. The
// record may already be durable, so the sequence is consumed: the next Append
// must NOT re-issue it. Gaps are allowed; reuse is not (P32-I13/I15).
func TestDurableSeq_NeverReusedAfterPostSyncError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// Simulate: write + sync succeed, close reports an error. The handle is
	// still released (a real Close error usually does), so the test does not
	// leak an open file that would block TempDir cleanup.
	store.closeFn = func(f *os.File) error {
		_ = f.Close()
		return errors.New("simulated close failure")
	}

	first := protection.AlertTransition{
		At: time.Now(), From: false, To: true, UnknownRate: 7, Threshold: 50,
	}
	if err := store.Append(context.Background(), first); err == nil {
		t.Fatal("precondition: the simulated close failure must surface as an error")
	}

	// The sequence was consumed even though the call reported an error.
	if store.lastSeq != 1 {
		t.Fatalf("P32-I15: seq must be consumed after a durable write, lastSeq=%d", store.lastSeq)
	}

	// Next append must allocate a STRICTLY greater sequence (no reuse).
	store.closeFn = (*os.File).Close
	second := protection.AlertTransition{
		At: time.Now().Add(time.Second), From: true, To: false, UnknownRate: 0, Threshold: 50,
	}
	if err := store.Append(context.Background(), second); err != nil {
		t.Fatalf("second append: %v", err)
	}
	if store.lastSeq != 2 {
		t.Fatalf("P32-I15: the next append must not reuse seq=1, got lastSeq=%d", store.lastSeq)
	}

	// And the durable file must show two distinct, increasing sequences.
	seqs := durableSeqs(t, path)
	if len(seqs) != 2 {
		t.Fatalf("expected 2 durable records, got %d (%v)", len(seqs), seqs)
	}
	if seqs[0] >= seqs[1] {
		t.Fatalf("P32-I13: sequences must strictly increase, got %v", seqs)
	}
	if seqs[0] == seqs[1] {
		t.Fatalf("P32-I13: duplicate sequence in the durable file: %v", seqs)
	}
}

// --- T-m4 ------------------------------------------------------------------
// P32-I15 recovery rule: lastSeq = max(meta.last_seq, max(record.seq)).
// A torn state (records ahead of the persisted watermark) must advance the
// watermark past every existing record — gaps allowed, reuse forbidden.
func TestDurableSeq_RecoveryTakesMaxOfWatermarkAndRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	// Meta watermark lags behind the records (as a torn append would leave it).
	seed := "{\"_meta\":true,\"file_dropped\":0,\"last_seq\":2}\n" +
		"{\"seq\":1,\"at\":\"2020-01-01T00:00:00Z\",\"from\":false,\"to\":true,\"unknown_rate\":1,\"threshold\":50}\n" +
		"{\"seq\":5,\"at\":\"2020-01-02T00:00:00Z\",\"from\":true,\"to\":false,\"unknown_rate\":0,\"threshold\":50}\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if store.lastSeq != 5 {
		t.Fatalf("P32-I15: recovery must take max(last_seq=2, max(record.seq)=5)=5, got %d", store.lastSeq)
	}
	// The next append must allocate 6 — beyond every existing record.
	if err := store.Append(context.Background(), protection.AlertTransition{
		At: time.Now(), From: false, To: true, UnknownRate: 9, Threshold: 50,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if store.lastSeq != 6 {
		t.Fatalf("P32-I15: next allocation must be 6 (never reuse 1..5), got %d", store.lastSeq)
	}
	seqs := durableSeqs(t, path)
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("P32-I13: sequences must strictly increase, got %v", seqs)
		}
	}
}

// durableSeqs returns the persisted sequences in durable order.
func durableSeqs(t *testing.T, path string) []int64 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	out := make([]int64, 0, len(lines))
	for _, ln := range lines {
		var probe struct {
			Seq int64 `json:"seq"`
		}
		if err := json.Unmarshal([]byte(ln), &probe); err != nil {
			t.Fatalf("line must be valid JSON: %v", err)
		}
		if probe.Seq == 0 {
			continue // meta line
		}
		out = append(out, probe.Seq)
	}
	return out
}
