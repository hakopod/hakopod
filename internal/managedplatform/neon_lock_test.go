//go:build linux || darwin || windows

package managedplatform

import "testing"

func TestNeonStateLockExcludesOtherRuntimeAndReleases(t *testing.T) {
	first := &NeonRuntime{config: NeonRuntimeConfig{StateDirectory: t.TempDir()}}
	second := &NeonRuntime{config: first.config}
	unlock, err := first.lock(testTenant)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	if unexpectedUnlock, err := second.lock(testTenant); err == nil {
		unexpectedUnlock()
		t.Fatal("a second runtime acquired the same tenant lock")
	}
	otherUnlock, err := second.lock("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("unrelated tenant lock failed: %v", err)
	}
	otherUnlock()
	unlock()
	unlock = nil
	unlocked, err := second.lock(testTenant)
	if err != nil {
		t.Fatalf("released tenant lock remained held: %v", err)
	}
	unlocked()
}
