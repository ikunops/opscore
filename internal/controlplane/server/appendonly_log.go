package server

// Phase 40 — the shared append-only persistence primitive.
//
// Two on-disk histories (the Phase 39 chain-digest ledger and the Phase 40
// anchor log) must obey exactly the same persistence discipline, and there must
// be exactly ONE implementation of that discipline (ADR-053 §3.1). A second
// copy would drift, and drift here silently changes what the evidence means —
// that is precisely the failure R205 had to repair in Phase 39.
//
// This primitive is deliberately MEANING-BLIND. It never interprets a line: the
// caller injects a classifier that maps raw bytes to a group number, so no
// vocabulary of any consumer exists in this file. Grouping, identity and
// conflict semantics all stay on the caller's side.
//
// The discipline (ADR-053 I1/I3 and R40-4, a generalisation of Phase 39's R205):
//
//   - an append is a TRUE append: bytes already on disk are never rewritten;
//   - the ONLY rewrite is an explicit prefix compaction, which copies every
//     surviving line verbatim and drops whole groups, never part of one;
//   - a line the classifier cannot read makes compaction FAIL-CLOSED — it is
//     refused and the file is left byte-identical. Trimming a prefix around
//     evidence we cannot read is exactly the "rebuild a clean file" failure the
//     discipline exists to prevent.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
)

// groupClassifier maps one raw line to the number of the group it belongs to.
// ok=false marks the line as unreadable by the caller; such a line is preserved
// verbatim and blocks every operation that would rewrite the file.
type groupClassifier func(raw []byte) (int64, bool)

// logLine is one physical line together with the group the caller assigned to
// it. Unreadable lines are kept verbatim with classified=false; they are never
// dropped, reordered, or rewritten.
type logLine struct {
	raw        []byte
	group      int64
	classified bool
}

// readLogLines returns every non-blank line of the file, in file order, trimmed
// of surrounding whitespace. ok reports whether ALL lines were classified; it is
// false as soon as one line cannot be read. A missing file yields no lines,
// ok=true and no error — absence is not corruption.
func readLogLines(path string, classify groupClassifier) ([]logLine, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, true, nil
		}
		return nil, false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var out []logLine
	ok := true
	for scanner.Scan() {
		trimmed := bytes.TrimSpace(scanner.Bytes())
		if len(trimmed) == 0 {
			continue
		}
		raw := append([]byte(nil), trimmed...) // the scanner reuses its buffer
		g, classified := classify(raw)
		if !classified {
			ok = false
		}
		out = append(out, logLine{raw: raw, group: g, classified: classified})
	}
	if serr := scanner.Err(); serr != nil {
		return nil, false, serr
	}
	return out, ok, nil
}

// appendLogLine performs a TRUE append: it opens for append only and writes one
// newline-terminated line, so no byte already on disk can be touched.
//
// The caller owns its own fail-closed checks and must have performed them before
// calling — this function cannot know what a refusal means for the caller's
// semantics (ADR-053 I2).
func appendLogLine(path string, line []byte) error {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return errors.New("append: refusing to write a blank line")
	}
	out := make([]byte, 0, len(trimmed)+1)
	out = append(out, trimmed...)
	out = append(out, '\n')

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, werr := f.Write(out); werr != nil {
		f.Close() // a failed write still releases the handle (R205 discipline)
		return werr
	}
	if serr := f.Sync(); serr != nil {
		f.Close()
		return serr
	}
	return f.Close()
}

// compactLogPrefixGroups drops the oldest whole GROUPS until at most keepGroups
// remain and copies every surviving line verbatim. It is the ONLY operation in
// this file that rewrites bytes. keepGroups <= 0 means unbounded (nothing is
// ever dropped).
//
// [R40-4] If any line cannot be classified the compaction is REFUSED and the
// file is left byte-identical: trimming a prefix around unreadable evidence is
// precisely the "rebuild a clean file" failure the discipline forbids, and both
// consumers inherit this guarantee from one place.
func compactLogPrefixGroups(path string, keepGroups int, classify groupClassifier) error {
	if keepGroups <= 0 {
		return nil
	}
	lines, ok, err := readLogLines(path, classify)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("compaction refused: %d unclassifiable line(s) present; rewriting the file would silently discard that evidence", countUnclassified(lines))
	}

	// Groups are ordered by first appearance — a log is written oldest-first, so
	// "the oldest groups" is the prefix of this order.
	order := make([]int64, 0, len(lines))
	seen := make(map[int64]bool, len(lines))
	for _, l := range lines {
		if !seen[l.group] {
			seen[l.group] = true
			order = append(order, l.group)
		}
	}
	if len(order) <= keepGroups {
		return nil
	}
	drop := make(map[int64]bool, len(order)-keepGroups)
	for _, g := range order[:len(order)-keepGroups] {
		drop[g] = true
	}
	kept := make([]logLine, 0, len(lines))
	for _, l := range lines {
		if !drop[l.group] {
			kept = append(kept, l)
		}
	}
	if len(kept) == len(lines) {
		return nil
	}
	return rewriteLogLines(path, kept)
}

// rewriteLogLines replaces the file with exactly the given lines, each written
// verbatim plus one newline. It is only reached through compaction.
func rewriteLogLines(path string, lines []logLine) error {
	tmp := path + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	for _, l := range lines {
		buf := make([]byte, 0, len(l.raw)+1)
		buf = append(buf, l.raw...)
		buf = append(buf, '\n')
		if _, werr := out.Write(buf); werr != nil {
			out.Close()
			os.Remove(tmp)
			return werr
		}
	}
	if serr := out.Sync(); serr != nil {
		out.Close()
		os.Remove(tmp)
		return serr
	}
	if cerr := out.Close(); cerr != nil {
		os.Remove(tmp)
		return cerr
	}
	if rerr := os.Rename(tmp, path); rerr != nil {
		os.Remove(tmp)
		return rerr
	}
	return nil
}

func countUnclassified(lines []logLine) int {
	n := 0
	for _, l := range lines {
		if !l.classified {
			n++
		}
	}
	return n
}
