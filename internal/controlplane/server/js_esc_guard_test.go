package server

// T406 (ADR-094 / D6): the embedded console pages must never interpolate an
// unescaped value into innerHTML-bound template literals. The initial RED of
// this tripwire WAS the finding: ui.html defined no esc() at all and rendered
// target-host-controlled text (systemctl unit descriptions) and stored audit
// details straight into innerHTML — a managed host could run script in the
// admin's browser. dashboard.html had the esc() discipline; ui.html (written
// earlier) did not.
//
// Declared scope — a LINE-LEVEL tripwire, not a JS parser:
//   - every line containing `${` must go through esc()/jsq() OR every one of
//     its interpolations must match an explicitly enumerated safe shape
//     (internal class tokens, numbers/lengths, pre-escaped locals, conditional
//     literals, date formatting, and one composite opener whose body closes
//     with esc());
//   - inline-handler JS-string arguments (onclick="f('<here>')") must use
//     jsq(), never esc() — the browser decodes HTML entities BEFORE compiling
//     the JS, so &#39; would come back as a literal quote;
//   - floors make a vacuous pass impossible: esc() must be defined in both
//     files (jsq() in the dashboard) and used at least N times.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var t406SafeInterp = []*regexp.Regexp{
	// Internal class tokens / fixed label & state mappings.
	regexp.MustCompile(`^\{?(cls|dot|label|state)\}?$`),
	// Numbers, lengths, retained counts (never HTML).
	regexp.MustCompile(`^\{?\s*(n|N|ur|thr|win|since|cap|buf|dropped|fileDropped|retained|hs\.retained|txns\.length|m\[k\] \?\? 0)\s*\}?$`),
	// Pre-escaped locals: assigned via esc(...) on the lines that build them
	// (fileNote is a number-only composition).
	regexp.MustCompile(`^\{?(at|lat|cap|guard|detail|aTag|tag|fileNote)\}?$`),
	// Conditionals over safe booleans/numbers — the branches' nested
	// interpolations are checked recursively below.
	regexp.MustCompile(`^\s*(r\.killed|firing|r\.killed_at|x\.at)\s*\?`),
	regexp.MustCompile(`^\{?new Date\(`),
	// A composite opening whose body closes with esc()-wrapped parts.
	regexp.MustCompile(`^\{?$`),
}

// t406ExprSafe: an interpolation expression is safe when it escapes its own
// dynamic content (esc(/jsq( visible), when EVERY nested interpolation it
// contains is itself safe, or when it matches an explicitly enumerated safe
// shape.
func t406ExprSafe(expr string) bool {
	if strings.Contains(expr, "esc(") || strings.Contains(expr, "jsq(") {
		return true
	}
	for _, nested := range t406Interpolations(expr) {
		if !t406ExprSafe(nested) {
			return false
		}
	}
	for _, re := range t406SafeInterp {
		if re.MatchString(expr) {
			return true
		}
	}
	return false
}

func t406Interpolations(line string) []string {
	var out []string
	for {
		i := strings.Index(line, "${")
		if i < 0 {
			break
		}
		rest := line[i+2:]
		depth := 1
		j := 0
		for ; j < len(rest); j++ {
			switch rest[j] {
			case '{':
				depth++
			case '}':
				depth--
			}
			if depth == 0 {
				break
			}
		}
		if j >= len(rest) {
			out = append(out, rest) // unterminated on this line: the rest is the content
			break
		}
		out = append(out, rest[:j])
		line = rest[j+1:]
	}
	return out
}

func TestT406ConsoleTemplatesEscapeInterpolations(t *testing.T) {
	cases := []struct {
		file     string
		minEsc   int
		needJSQ  bool
	}{
		{"ui.html", 10, false},
		{"dashboard.html", 15, true},
	}
	for _, tc := range cases {
		data, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("T406: read %s: %v", tc.file, err)
		}
		text := string(data)
		if !strings.Contains(text, "function esc(") {
			t.Errorf("T406: %s must define esc() (the HTML-context escaper)", tc.file)
		}
		if tc.needJSQ && !strings.Contains(text, "function jsq(") {
			t.Errorf("T406: %s must define jsq() (the inline-handler JS-string escaper)", tc.file)
		}
		if n := strings.Count(text, "esc(") + strings.Count(text, "jsq("); n < tc.minEsc {
			t.Errorf("T406: %s uses esc/jsQ only %d times (< %d) — extraction or discipline broke", tc.file, n, tc.minEsc)
		}

		for i, line := range strings.Split(text, "\n") {
			if !strings.Contains(line, "${") {
				continue
			}
			// Inline handler arguments live in a JS string inside an HTML
			// attribute: only jsq() escapes BOTH layers.
			if strings.Contains(line, `onclick="`) {
				if !strings.Contains(line, "jsq(") || strings.Contains(line, "esc(") {
					t.Errorf("T406: %s:%d inline handler must use jsq(), not esc(): %s", tc.file, i+1, strings.TrimSpace(line))
				}
				continue
			}
			if strings.Contains(line, "esc(") || strings.Contains(line, "jsq(") {
				continue
			}
			for _, expr := range t406Interpolations(line) {
				if !t406ExprSafe(expr) {
					t.Errorf("T406: %s:%d unescaped interpolation ${%s}: %s", tc.file, i+1, expr, strings.TrimSpace(line))
				}
			}
		}
	}
}
