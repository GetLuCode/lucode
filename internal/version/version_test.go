package version

import "testing"

func TestCurrentDescribesTheProcess(t *testing.T) {
	v := Current("runner")
	if v.Role != "runner" || v.Go == "" || v.OS == "" || v.Short == "" || v.StartedAt.IsZero() {
		t.Errorf("incomplete: %+v", v)
	}
	// test binaries carry no vcs info: it must say so instead of inventing a commit
	if v.Commit == "" && v.Short != "unknown" {
		t.Errorf("short = %q without a commit", v.Short)
	}
}
