package core

import (
	"errors"
	"strings"
)

// ErrUnsafeArgument indicates an operation input would be reinterpreted as a
// command flag or shell metacharacter by the underlying executable, bypassing
// the builtin's whitelist. Operation inputs are passed as distinct argv elements
// (never through a shell: exec.Command(args...)), so the only injection vector
// is a value that begins with '-' (re-parsed as a flag, e.g. "useradd -o -u 0"
// or "apt-get --allow-unauthenticated") or that carries shell metacharacters.
var ErrUnsafeArgument = errors.New("opscore: unsafe argument (flag or metacharacter not allowed)")

// SafeToken rejects strings that could be reinterpreted as a command flag or
// shell metacharacter. tok must be a single argv element (no whitespace). A
// leading '-' is rejected because the target program would parse it as a flag.
func SafeToken(tok string) error {
	if tok == "" {
		return errors.New("opscore: empty argument not allowed")
	}
	if strings.HasPrefix(tok, "-") {
		return ErrUnsafeArgument
	}
	if strings.ContainsAny(tok, " \t\n\r\f\v;|&$`()<>\"'\\") {
		return ErrUnsafeArgument
	}
	return nil
}
