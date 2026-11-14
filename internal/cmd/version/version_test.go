package version

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrerfcsantos/orbit/internal/app"
	"github.com/andrerfcsantos/orbit/internal/platform/build"
)

func newCommand(t *testing.T, runtime *app.Runtime) (*bytes.Buffer, func(args ...string) error) {
	t.Helper()

	var out bytes.Buffer
	cmd := New(func() *app.Runtime { return runtime })
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	return &out, func(args ...string) error {
		cmd.SetArgs(args)
		return cmd.Execute()
	}
}

func TestVersionPrintsBuildInfo(t *testing.T) {
	runtime := &app.Runtime{BuildInfo: build.NewInfo("v2.3.4", "deadbeef", "2026-05-06T07:08:09Z")}
	out, run := newCommand(t, runtime)

	if err := run(); err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	for _, want := range []string{"v2.3.4", "deadbeef", "2026-05-06T07:08:09Z"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestVersionJSONMatchesAPIShape(t *testing.T) {
	runtime := &app.Runtime{BuildInfo: build.NewInfo("v2.3.4", "deadbeef", "2026-05-06T07:08:09Z")}
	out, run := newCommand(t, runtime)

	if err := run("--json"); err != nil {
		t.Fatalf("Execute(--json) = %v", err)
	}

	// The CLI's JSON and GET /api/version must stay the same shape, since both come
	// from the same service and scripts may consume either.
	var info build.Info
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if info != *runtime.BuildInfo {
		t.Errorf("JSON = %#v, want %#v", info, *runtime.BuildInfo)
	}
}

func TestVersionRequiresRuntime(t *testing.T) {
	_, run := newCommand(t, nil)
	if err := run(); err == nil {
		t.Error("Execute() with no runtime = nil, want an error")
	}
}
