package routing

import (
	"errors"
	"fmt"
	"strings"
)

var (
	errHarnessModelIncompatible = errors.New("routing: harness model incompatible")
	errProfilePinConflict       = errors.New("routing: profile pin conflict")
	errNoProfileCandidate       = errors.New("routing: no profile candidate")
)

// ErrHarnessModelIncompatible reports an explicit Harness+Model pin that the
// harness allow-list cannot serve.
type ErrHarnessModelIncompatible struct {
	// Harness is the canonical harness name supplied by the caller.
	Harness string
	// Model is the exact concrete model pin supplied by the caller.
	Model string
	// SupportedModels is the harness allow-list that rejected Model.
	SupportedModels []string
}

func (e ErrHarnessModelIncompatible) Error() string {
	return fmt.Sprintf("model %q is not supported by harness %q; supported models: %s", e.Model, e.Harness, strings.Join(e.SupportedModels, ", "))
}

func (e ErrHarnessModelIncompatible) Is(target error) bool {
	switch target.(type) {
	case ErrHarnessModelIncompatible, *ErrHarnessModelIncompatible:
		return true
	default:
		return errors.Is(errHarnessModelIncompatible, target)
	}
}

func (e ErrHarnessModelIncompatible) Unwrap() error {
	return errHarnessModelIncompatible
}

// ErrProfilePinConflict reports an explicit Profile whose placement constraint
// contradicts another explicit caller pin.
type ErrProfilePinConflict struct {
	// Profile is the explicit profile requested by the caller.
	Profile string
	// ConflictingPin names the explicit pin that violates the profile, such as
	// "Harness=claude" or "Model=local-model".
	ConflictingPin string
	// ProfileConstraint is a short description of the profile placement rule,
	// such as "local-only" or "subscription-only".
	ProfileConstraint string
}

func (e ErrProfilePinConflict) Error() string {
	return fmt.Sprintf("profile %q requires %s but conflicts with %s", e.Profile, e.ProfileConstraint, e.ConflictingPin)
}

func (e ErrProfilePinConflict) Is(target error) bool {
	switch target.(type) {
	case ErrProfilePinConflict, *ErrProfilePinConflict:
		return true
	default:
		return errors.Is(errProfilePinConflict, target)
	}
}

func (e ErrProfilePinConflict) Unwrap() error {
	return errProfilePinConflict
}

// ErrNoProfileCandidate reports that a profile's hard placement requirement
// could not be satisfied by any routed candidate.
type ErrNoProfileCandidate struct {
	Profile           string
	MissingCapability string
	Rejected          int
}

func (e ErrNoProfileCandidate) Error() string {
	return fmt.Sprintf("profile %q has no candidate satisfying %s: %d candidates rejected", e.Profile, e.MissingCapability, e.Rejected)
}

func (e ErrNoProfileCandidate) Is(target error) bool {
	switch target.(type) {
	case ErrNoProfileCandidate, *ErrNoProfileCandidate:
		return true
	default:
		return errors.Is(errNoProfileCandidate, target)
	}
}

func (e ErrNoProfileCandidate) Unwrap() error {
	return errNoProfileCandidate
}
