package config

import (
	"bytes"
	"strings"
	"testing"

	appconfig "github.com/andrerfcsantos/orbit/internal/app/config"
	platformconfig "github.com/andrerfcsantos/orbit/internal/platform/config"
	"github.com/andrerfcsantos/orbit/internal/transport/tui/styles"
)

// newTestManager writes to a temporary config dir, so a test never touches the
// developer's own configuration.
func newTestManager(t *testing.T) *platformconfig.Manager {
	t.Helper()
	return platformconfig.New("orbit",
		platformconfig.WithEnvPrefix("ORBIT"),
		platformconfig.WithUserConfigDir(t.TempDir()),
		platformconfig.WithCatalog(platformconfig.NewCatalog(append(platformconfig.DefaultCatalog().Keys(), testSecretKey)...)),
	)
}

// testSecretKey stands in for a credential: orbit ships none yet, but the rules
// for one hold already.
var testSecretKey = platformconfig.KeyDef{
	Key:         "provider.access-key",
	Description: "A provider credential",
	Default:     "",
	Secret:      true,
}

// serviceWithNoCatalog describes no key, so every lookup falls to the hints.
func serviceWithNoCatalog() appconfig.Service { return appconfig.Service{} }

// isSecret's hint list only ever sees keys the catalog does not know: a declared
// key answers from KeyDef.Secret. Those unknown keys are the forward-compatible
// ones nobody has classified yet, which is exactly when guessing wrong prints a
// credential to the terminal.
func TestUnknownCredentialShapedKeysAreMasked(t *testing.T) {
	for _, key := range []string{
		"API_TOKEN",
		"provider.api_key",
		"provider.api-key",
		"provider.apikey",
		"Some.Password",
		"vendor.CLIENT_SECRET",
		"thing.credential",
		"db.pwd",
		"provider.genius.user",
		"vendor.username",
	} {
		if !isSecret(serviceWithNoCatalog(), key) {
			t.Errorf("%q was not treated as a secret", key)
		}
	}
}

// Over-masking a harmless key is cheap, but sweeping up a whole namespace is
// not: "valkey" contains "key", and cache.valkey.* is mostly addresses and flags.
func TestOrdinaryKeysAreNotMasked(t *testing.T) {
	for _, key := range []string{
		"cache.valkey.address",
		"cache.valkey.tls",
		"log.level",
		"server.port",
		"git.release-tag.prefix",
		"monkey.business",
	} {
		if isSecret(serviceWithNoCatalog(), key) {
			t.Errorf("%q was masked but holds no credential", key)
		}
	}
}

// Credential keys are flagless precisely so a secret never reaches a command
// line. Accepting one as a positional argument here is the same exposure by
// another route, and masking the echoed confirmation does nothing about the
// shell history and process list that already have it.
func TestSecretsCannotBeSetFromTheCommandLine(t *testing.T) {
	service := appconfig.Service{Manager: newTestManager(t)}

	var out bytes.Buffer
	err := set(&out, styles.For(&out), service, testSecretKey.Key, "sk-live-value", false)
	if err == nil {
		t.Fatal("a secret was accepted as a positional argument")
	}
	if strings.Contains(err.Error(), "sk-live-value") {
		t.Errorf("the rejection echoed the secret back: %v", err)
	}
	// The remedy has to be in the message, or the next step is to pass it anyway.
	for _, want := range []string{"ORBIT_PROVIDER_ACCESS_KEY", "shell history"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("rejection should mention %q, got: %v", want, err)
		}
	}
	if out.Len() != 0 {
		t.Errorf("nothing should be written on rejection, got %q", out.String())
	}
}

// The gate reads the catalog, not the masking hints: those are tuned for display,
// where over-matching costs nothing, and ".key" in them would otherwise refuse a
// perfectly ordinary unknown key.
func TestUnknownKeysAreStillSettable(t *testing.T) {
	var out bytes.Buffer
	if err := set(&out, styles.For(&out), appconfig.Service{Manager: newTestManager(t)},
		"future.key", "value", true); err != nil {
		t.Errorf("an unknown key was refused: %v", err)
	}
}

// Clearing a secret exposes nothing, so it stays available.
func TestSecretsCanBeCleared(t *testing.T) {
	var out bytes.Buffer
	if err := set(&out, styles.For(&out), appconfig.Service{Manager: newTestManager(t)},
		testSecretKey.Key, "", false); err != nil {
		t.Errorf("clearing a secret was rejected: %v", err)
	}
}

// A non-secret key is unaffected.
func TestOrdinaryKeysCanStillBeSet(t *testing.T) {
	var out bytes.Buffer
	if err := set(&out, styles.For(&out), appconfig.Service{Manager: newTestManager(t)},
		platformconfig.KeyLogLevel, "debug", false); err != nil {
		t.Errorf("setting an ordinary key failed: %v", err)
	}
	if !strings.Contains(out.String(), "debug") {
		t.Errorf("the confirmation should show the value, got %q", out.String())
	}
}
