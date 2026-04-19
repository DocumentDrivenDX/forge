// Package configinit is a public marker package whose sole purpose is to
// trigger the init() in internal/config that registers the ConfigPath-backed
// ServiceConfig loader with the root agent package.
//
// The agent module's config package is internal (CONTRACT-003 keeps the
// surface area minimal), so external callers cannot import it directly. They
// import this package with a blank import instead:
//
//	import _ "github.com/DocumentDrivenDX/agent/configinit"
//
// With this import in place, agent.New(opts) can auto-load configuration when
// opts.ServiceConfig is nil but opts.ConfigPath is set, without requiring the
// caller to construct a ServiceConfig themselves.
//
// This package exposes no symbols. Importing it is the entire API.
package configinit

import (
	// Triggers internal/config's init() which calls
	// agent.RegisterConfigLoader, installing the directory-based loader used
	// by agent.New when opts.ServiceConfig is nil.
	_ "github.com/DocumentDrivenDX/agent/internal/config"
)
