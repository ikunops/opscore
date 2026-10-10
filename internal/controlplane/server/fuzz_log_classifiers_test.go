package server

// Robustness fuzzing (added 2026-10-08; NOT part of any phase's contract).
//
// The seven log-line classifiers are the FIRST code every loader runs on bytes
// that live in the export directory — and the threat model gives the adversary
// write access to that directory (ADR-063/071's adversary). Their contract is
// narrow: "return a group id and true, or refuse". The project pins it with
// hand-written malformed cases (T335 / T358 / T378); native Go fuzzing explores
// the same space systematically, and a PANIC here would be a crash reachable from
// one crafted ledger line.
//
// These are ordinary Go fuzz targets: `go test` runs the seed corpus only (cheap,
// so the suite keeps its meaning and its runtime), while
// `go test -fuzz=FuzzLogClassifiers -fuzztime=90s` explores. The property checked
// is the one that matters for a read path: NO PANIC, for any bytes, through the
// classifier AND through the family's struct decoding — the exact pair every
// loader performs.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fuzzFamily pairs one classifier with the struct its loader decodes the same
// line into. Keeping them together is the point: a classifier that accepts a line
// the decoder then chokes on is a real defect, and it is invisible to either half
// alone.
type fuzzFamily struct {
	name     string
	classify groupClassifier
	decode   func(raw []byte)
}

func fuzzFamilies() []fuzzFamily {
	return []fuzzFamily{
		{"anchor", anchorGroupOf, func(raw []byte) { var v anchorEntry; _ = json.Unmarshal(raw, &v) }},
		{"ledger", ledgerGroupOf, func(raw []byte) { var v ledgerEntry; _ = json.Unmarshal(raw, &v) }},
		{"key_lifecycle", keyLifecycleGroupOf, func(raw []byte) { var v keyLifecycleEntry; _ = json.Unmarshal(raw, &v) }},
		{"destruction", destructionGroupOf, func(raw []byte) { var v destructionEntry; _ = json.Unmarshal(raw, &v) }},
		{"verification", verificationGroupOf, func(raw []byte) { var v verificationLogEntry; _ = json.Unmarshal(raw, &v) }},
		{"acceptance", acceptanceGroupOf, func(raw []byte) { var v acceptanceEntry; _ = json.Unmarshal(raw, &v) }},
		{"decision", decisionGroupOf, func(raw []byte) { var v decisionRecord; _ = json.Unmarshal(raw, &v) }},
	}
}

// fuzzSeeds are the shapes the hand-written tests use: a plausible member of each
// family, the malformed forms, and the boundary shapes (empty, bare braces, a
// wrong-typed field) that the classifier has to refuse or survive.
func fuzzSeeds() [][]byte {
	return [][]byte{
		[]byte(``),
		[]byte(`{}`),
		[]byte(`[]`),
		[]byte(`null`),
		[]byte(`{not json`),
		[]byte(`{"anchor_seq":1,"kind":"publication"}`),
		[]byte(`{"anchor_seq":0}`),
		[]byte(`{"anchor_seq":-1}`),
		[]byte(`{"anchor_seq":9223372036854775807}`),
		[]byte(`{"publication_id":1,"manifest_digest":"ab"}`),
		[]byte(`{"event_seq":1,"event_type":"activated","key_id":"k"}`),
		[]byte(`{"destruction_seq":1,"kind":"ledger_compaction"}`),
		[]byte(`{"report_seq":1,"overall":"attested"}`),
		[]byte(`{"entry_seq":1,"record_seq":1}`),
		[]byte(`{"seq":1,"digest":"ab"}`),
		[]byte(`{"anchor_seq":"not-a-number"}`),
		[]byte(`{"anchor_seq":1,"targets":[{"from_seq":1,"to_seq":2}]}`),
		[]byte("\x00\xff\xfe"),
		[]byte(`{"v":1}`),
	}
}

// FuzzLogClassifiers is the pure target: every classifier against every family's
// decoder, for arbitrary bytes. A panic fails the fuzz run with the exact input.
func FuzzLogClassifiers(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add(s)
	}
	fams := fuzzFamilies()
	f.Fuzz(func(t *testing.T, raw []byte) {
		for _, fam := range fams {
			id, ok := fam.classify(raw)
			if !ok {
				continue
			}
			// Accepted: the loader will now decode this very line. Neither step may
			// panic, and re-reading the same bytes must agree (the loaders classify
			// each line once, but a chain walk revisits the bytes).
			if id2, ok2 := fam.classify(raw); ok2 != ok || id2 != id {
				t.Fatalf("%s: classifier is not a function of its input: (%d,%v) then (%d,%v)", fam.name, id, ok, id2, ok2)
			}
			fam.decode(raw)
		}
	})
}

// FuzzReadLogLines drives the SHARED primitive that every loader uses to turn a
// file into classified lines: it must never panic, and its three outputs must
// agree (the unclassifiable count it reports is what every loader turns into a
// loud fail-closed refusal).
func FuzzReadLogLines(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add(s)
	}
	dir := f.TempDir()
	path := filepath.Join(dir, "fuzz-ledger.jsonl")
	fams := fuzzFamilies()
	f.Fuzz(func(t *testing.T, raw []byte) {
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Skip("cannot write the probe file")
		}
		for _, fam := range fams {
			lines, ok, err := readLogLines(path, fam.classify)
			if err != nil {
				continue
			}
			if !ok && countUnclassified(lines) == 0 {
				t.Fatalf("%s: readLogLines refused the file but reports no unclassifiable line", fam.name)
			}
			if ok && countUnclassified(lines) != 0 {
				t.Fatalf("%s: readLogLines accepted the file while reporting %d unclassifiable line(s)", fam.name, countUnclassified(lines))
			}
		}
	})
}
