package agent

import (
	"errors"
	"fmt"
	"strings"
)

var (
	errHarnessModelIncompatible = errors.New("agent: harness model incompatible")
	errProfilePinConflict       = errors.New("agent: profile pin conflict")
	errNoProfileCandidate       = errors.New("agent: no profile candidate")
	errUnknownProfile           = errors.New("agent: unknown profile")
)

// DecisionWithCandidates is implemented by routing errors that retain the
// evaluated candidate trace for a failed ResolveRoute call.
type DecisionWithCandidates interface {
	error
	// RouteCandidates returns the evaluated candidates that led to the error.
	RouteCandidates() []RouteCandidate
}

type routeDecisionError struct {
	err        error
	candidates []RouteCandidate
}

func (e *routeDecisionError) Error() string {
	return e.err.Error()
}

func (e *routeDecisionError) Unwrap() error {
	return e.err
}

func (e *routeDecisionError) RouteCandidates() []RouteCandidate {
	return append([]RouteCandidate(nil), e.candidates...)
}

// ErrHarnessModelIncompatible reports an explicit Harness+Model pin that the
// harness allow-list cannot serve.
//
// DDx preflight callers should use errors.As to extract Harness, Model, and
// SupportedModels for worker logs or bead failure records. errors.Is matches a
// zero-value ErrHarnessModelIncompatible, even after callers wrap the error with
// fmt.Errorf and %w.
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
//
// DDx preflight callers should use errors.As to extract Profile,
// ConflictingPin, and ProfileConstraint for worker logs or bead failure
// records. errors.Is matches a zero-value ErrProfilePinConflict, even after
// callers wrap the error with fmt.Errorf and %w.
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

// ErrUnknownProfile reports an explicit profile name that is not present in
// the model catalog.
type ErrUnknownProfile struct {
	Profile string
}

func (e ErrUnknownProfile) Error() string {
	return fmt.Sprintf("unknown routing profile %q", e.Profile)
}

func (e ErrUnknownProfile) Is(target error) bool {
	switch target.(type) {
	case ErrUnknownProfile, *ErrUnknownProfile:
		return true
	default:
		return errors.Is(errUnknownProfile, target)
	}
}

func (e ErrUnknownProfile) Unwrap() error {
	return errUnknownProfile
}
