package network

import (
	"bytes"
	"errors"
	"github.com/sijms/go-ora/v3/configurations"
	"github.com/sijms/go-ora/v3/trace"
	"testing"
)

func limitedSession(limit int, data []byte) *Session {
	s := NewSession(&configurations.ConnectionConfig{}, trace.NilTracer())
	s.SetReadLimit(limit)
	s.inBuffer = bytes.NewBuffer(data)
	return s
}
func TestReadLimitRejectsAdvertisedAllocation(t *testing.T) {
	s := limitedSession(1024, nil)
	if _, err := s.read(int(^uint(0) >> 1)); !errors.Is(err, ErrReadLimit) {
		t.Fatal(err)
	}
	if err := s.readAll(int(^uint(0) >> 1)); !errors.Is(err, ErrReadLimit) {
		t.Fatal(err)
	}
}
func TestReadLimitCLRRejectsChunkBeforeReading(t *testing.T) {
	s := limitedSession(4, []byte{0xfe, 5, 1, 2, 3, 4, 5, 0})
	if _, err := s.GetClr(); !errors.Is(err, ErrReadLimit) {
		t.Fatal(err)
	}
	if s.inBuffer.Len() != 6 {
		t.Fatal("oversized body consumed", s.inBuffer.Len())
	}
}
func TestReadLimitCLRCumulativeAppend(t *testing.T) {
	s := limitedSession(4, []byte{0xfe, 3, 1, 2, 3, 3, 4, 5, 6, 0})
	if _, err := s.GetClr(); !errors.Is(err, ErrReadLimit) {
		t.Fatal(err)
	}
	if s.inBuffer.Len() != 4 {
		t.Fatal("oversized cumulative body consumed")
	}
}
func TestReadLimitZeroRetainsValues(t *testing.T) {
	s := limitedSession(0, []byte{0xfe, 3, 1, 2, 3, 3, 4, 5, 6, 0})
	v, err := s.GetClr()
	if err != nil || len(v) != 6 {
		t.Fatal(v, err)
	}
}
func TestReadLimitAppendArithmetic(t *testing.T) {
	s := limitedSession(8, nil)
	for _, pair := range [][2]int{{7, 2}, {-1, 1}, {1, -1}, {int(^uint(0) >> 1), 1}} {
		if !errors.Is(s.CheckReadSize(pair[0], pair[1]), ErrReadLimit) {
			t.Fatal(pair)
		}
	}
	if s.CheckReadSize(4, 4) != nil {
		t.Fatal("exact bound rejected")
	}
}

func TestReadLimitPacketAccumulation(t *testing.T) {
	s := limitedSession(8, nil)
	s.lastPacket.Write(make([]byte, 7))
	if !errors.Is(s.readAll(2), ErrReadLimit) {
		t.Fatal("packet append accepted")
	}
}
