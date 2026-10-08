//go:build unix

package cluster

import (
	"os"
	"syscall"
	"testing"
)

type runtimeProfileOwnerInfo struct {
	os.FileInfo
	stat any
}

func (i runtimeProfileOwnerInfo) Sys() any { return i.stat }

func TestRuntimeProfileFileAuthorityOwner(t *testing.T) {
	for _, uid := range []uint32{0, uint32(os.Geteuid())} {
		if !runtimeProfileFileOwnerAllowed(runtimeProfileOwnerInfo{stat: &syscall.Stat_t{Uid: uid}}) {
			t.Fatal("an authorized file owner was rejected")
		}
	}
	other := uint32(os.Geteuid()) + 1
	if other == 0 {
		other = 1
	}
	if runtimeProfileFileOwnerAllowed(runtimeProfileOwnerInfo{stat: &syscall.Stat_t{Uid: other}}) {
		t.Fatal("another user can supply runtime authority")
	}
	if runtimeProfileFileOwnerAllowed(runtimeProfileOwnerInfo{}) {
		t.Fatal("an unverified file owner was accepted")
	}
}
