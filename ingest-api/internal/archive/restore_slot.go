package archive

import "sync"

type restoreSlot struct {
	mutex            sync.Mutex
	runningRestoreID string
	stopRequested    bool
}

func (slot *restoreSlot) acquire(restoreID string) bool {
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	if slot.runningRestoreID != "" {
		return false
	}
	slot.runningRestoreID = restoreID
	slot.stopRequested = false
	return true
}

func (slot *restoreSlot) release() {
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	slot.runningRestoreID = ""
	slot.stopRequested = false
}

func (slot *restoreSlot) requestStop(restoreID string) bool {
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	if slot.runningRestoreID == "" || slot.runningRestoreID != restoreID {
		return false
	}
	slot.stopRequested = true
	return true
}

func (slot *restoreSlot) finish(finalize func(stopped bool)) {
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	finalize(slot.stopRequested)
	slot.runningRestoreID = ""
	slot.stopRequested = false
}
