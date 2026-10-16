// Copyright 2026 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package boot

import (
	"runtime"
	"testing"
	"time"
)

func TestEventCheckpointDoesNotWaitForContainer(t *testing.T) {
	l := &Loader{}
	l.mu.Lock()
	locked := true
	defer func() {
		if locked {
			l.mu.Unlock()
		}
	}()
	cm := &containerManager{l: l}
	started := make(chan struct{})
	eventDone := make(chan error, 1)
	go func() {
		close(started)
		eventDone <- cm.Event(&EventArgs{CID: "removed"}, &EventOut{})
	}()
	<-started
	runtime.Gosched()

	checkpointDone := make(chan struct{})
	go func() {
		l.saveMu.Lock()
		l.saveMu.Unlock()
		close(checkpointDone)
	}()
	select {
	case <-checkpointDone:
	case <-time.After(2 * time.Second):
		t.Error("checkpoint waited for an Event blocked on the container lifecycle lock")
	}
	l.mu.Unlock()
	locked = false
	select {
	case err := <-eventDone:
		if err == nil {
			t.Error("Event succeeded for a removed container")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Event did not finish after releasing the container lock")
	}
	<-checkpointDone
}

func TestEventCPURejectsRetiredContainer(t *testing.T) {
	l, cleanup, err := createLoader(testConfig(), testSpec())
	if err != nil {
		t.Fatalf("creating loader: %v", err)
	}
	defer cleanup()
	defer l.Destroy()
	defer l.ctrl.srv.Stop(time.Second)
	const cid = "foo"
	if _, err := l.pidsCount(cid); err != nil {
		t.Fatalf("initial Event existence check: %v", err)
	}
	l.k.AccountContainerCPU(map[string]uint64{cid: 100}, "sentry", 0, false, false)
	l.mu.Lock()
	l.k.RetireContainerCPU(cid)
	delete(l.processes, execID{cid: cid})
	l.mu.Unlock()
	cm := &containerManager{l: l}
	var out EventOut
	if err := cm.cpuStats(&EventArgs{CID: cid}, &out); err == nil {
		t.Fatalf("CPU snapshot succeeded after retirement, total=%d", out.Event.Data.CPU.Usage.Total)
	}
}
