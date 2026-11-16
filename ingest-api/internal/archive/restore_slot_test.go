package archive

import "testing"

func TestSlotRefusesSecondAcquireWhileRunning(t *testing.T) {
	var slot restoreSlot
	if !slot.acquire("first") {
		t.Fatal("first acquire refused")
	}
	if slot.acquire("second") {
		t.Fatal("second acquire allowed while one is running")
	}
}

func TestSlotFreesAfterFinish(t *testing.T) {
	var slot restoreSlot
	slot.acquire("first")
	slot.finish(func(stopped bool) {})
	if !slot.acquire("second") {
		t.Fatal("acquire refused after finish")
	}
}

func TestSlotFreesAfterRelease(t *testing.T) {
	var slot restoreSlot
	slot.acquire("first")
	slot.release()
	if !slot.acquire("second") {
		t.Fatal("acquire refused after release")
	}
}

func TestSlotRequestStopOnlyForRunningRestore(t *testing.T) {
	var slot restoreSlot
	slot.acquire("first")
	if slot.requestStop("other") {
		t.Fatal("stop accepted for a restore that is not running")
	}
	var stopped bool
	slot.finish(func(stoppedValue bool) { stopped = stoppedValue })
	if stopped {
		t.Fatal("finish reported stopped without a stop request")
	}
	slot.acquire("second")
	if !slot.requestStop("second") {
		t.Fatal("stop refused for the running restore")
	}
	slot.finish(func(stoppedValue bool) { stopped = stoppedValue })
	if !stopped {
		t.Fatal("finish did not report the stop request")
	}
}

func TestSlotRequestStopRefusedWhenIdle(t *testing.T) {
	var slot restoreSlot
	if slot.requestStop("") {
		t.Fatal("stop accepted on an idle slot")
	}
}
