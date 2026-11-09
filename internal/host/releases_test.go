package host

import (
	"errors"
	"net/http"
	"testing"
)

func TestStableChannelMissingOnlyOn404(t *testing.T) {
	if !stableChannelMissing(&httpStatusError{code: http.StatusNotFound}) {
		t.Fatal("404 must count as a missing stable release")
	}
	if stableChannelMissing(&httpStatusError{code: http.StatusForbidden}) {
		t.Fatal("non-404 statuses must not count")
	}
	if stableChannelMissing(errors.New("connect refused")) {
		t.Fatal("non-status errors must not count")
	}
	// The raw transport text stays stable: diagnostics quote it verbatim.
	err := error(&httpStatusError{code: http.StatusNotFound})
	if err.Error() != "release source HTTP 404" {
		t.Fatalf("status error text changed: %q", err.Error())
	}
}

func TestDefaultRuntimeChannelIsDev(t *testing.T) {
	// The stable runtime train is gated on G1 acceptance; until a full
	// release exists, /releases/latest 404s and a stable default would
	// block every fresh install. Revert this default when stable opens.
	prefs := defaultPreferences()
	if err := prefs.validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	if prefs.RuntimeChannel != "dev" {
		t.Fatalf("fresh installs must default to dev while stable is empty, got %q", prefs.RuntimeChannel)
	}
}
