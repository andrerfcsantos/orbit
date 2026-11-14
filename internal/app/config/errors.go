package config

import "errors"

// errNoManager reports a Service built without a Manager, which is a wiring
// mistake rather than a user error.
var errNoManager = errors.New("config service has no manager")
