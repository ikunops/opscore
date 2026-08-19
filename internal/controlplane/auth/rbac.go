package auth

import (
	"errors"

	"github.com/YuDong999/opscore/internal/storage"
)

// Authorization outcomes surfaced to callers (e.g. the HTTP middleware).
var (
	// ErrOperationNotFound means the named operation is not registered.
	ErrOperationNotFound = errors.New("auth: operation not found")
	// ErrOperationDisabled means the operation exists but is administratively off.
	ErrOperationDisabled = errors.New("auth: operation disabled")
	// ErrForbidden means the operation exists but the subject is not granted it.
	ErrForbidden = errors.New("auth: forbidden")
)

// Can reports whether username is a member of at least one role that grants the
// named operation. It is the low-level membership check (ignores enabled state).
// A missing user fails closed (returns false, nil) — callers should validate the
// subject via a token first.
func Can(stor storage.Storage, username, operation string) (bool, error) {
	u, err := stor.Users().GetByUsername(username)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return false, nil // fail closed: unknown subject
		}
		return false, err
	}
	roles, err := stor.Users().Roles(u.ID)
	if err != nil {
		return false, err
	}
	for _, r := range roles {
		ops, err := stor.Roles().Operations(r.ID)
		if err != nil {
			return false, err
		}
		for _, op := range ops {
			if op.Name == operation {
				return true, nil
			}
		}
	}
	return false, nil
}

// Authorize is the full gate used before executing an operation. It verifies the
// operation is registered and enabled, then checks the subject's role grants.
// Returns one of ErrOperationNotFound / ErrOperationDisabled / ErrForbidden.
func Authorize(stor storage.Storage, username, operation string) error {
	op, err := stor.Operations().GetByName(operation)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ErrOperationNotFound
		}
		return err
	}
	if !op.Enabled {
		return ErrOperationDisabled
	}
	ok, err := Can(stor, username, operation)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}
