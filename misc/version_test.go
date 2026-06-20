package misc

import "testing"

func TestVersionAccessors(t *testing.T) {
	t.Parallel()

	if GetAppName() == "" {
		t.Fatal("GetAppName() returned empty")
	}
	if GetVersion() == "" {
		t.Fatal("GetVersion() returned empty")
	}
	_ = GetGitHash()
}
