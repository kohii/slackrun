package slackapp

import (
	"sync"
	"testing"
)

func TestRestartGate_BusyLeavesAdmissionOpen(t *testing.T) {
	var gate restartGate
	release, ok := gate.admit()
	if !ok {
		t.Fatal("initial dispatch rejected")
	}
	if active, ready := gate.prepare(); ready || active != 1 {
		t.Fatalf("prepare while busy = (%d, %v), want (1, false)", active, ready)
	}
	releaseSecond, ok := gate.admit()
	if !ok {
		t.Fatal("busy prepare closed admission")
	}
	releaseSecond()
	release()
	if active, ready := gate.prepare(); !ready || active != 0 {
		t.Fatalf("prepare when idle = (%d, %v), want (0, true)", active, ready)
	}
	if _, ok := gate.admit(); ok {
		t.Fatal("dispatch admitted after prepare")
	}
	if active, ready := gate.prepare(); !ready || active != 0 {
		t.Fatalf("repeated prepare = (%d, %v), want (0, true)", active, ready)
	}
}

func TestRestartGate_AdmitAndPrepareAreAtomic(t *testing.T) {
	for i := 0; i < 1000; i++ {
		var gate restartGate
		start := make(chan struct{})
		var wg sync.WaitGroup
		var admitted, ready bool
		var release func()
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			var ok bool
			release, ok = gate.admit()
			admitted = ok
		}()
		go func() {
			defer wg.Done()
			<-start
			_, ready = gate.prepare()
		}()
		close(start)
		wg.Wait()
		if release != nil {
			release()
		}
		if admitted && ready {
			t.Fatal("dispatch admission and restart preparation both succeeded")
		}
	}
}
