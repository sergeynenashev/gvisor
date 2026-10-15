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

package kernel

import (
	"bytes"
	"context"
	"testing"

	"gvisor.dev/gvisor/pkg/state"
)

func TestContainerCPUAccounting(t *testing.T) {
	t.Run("only new host CPU follows new guest CPU", func(t *testing.T) {
		k := &Kernel{}
		first := k.AccountContainerCPU(map[string]uint64{"app": 10, "side": 90}, "cgroup", 100, true, true)
		if first["app"] != 10 || first["side"] != 90 {
			t.Fatalf("initial allocation = %v, want app=10 side=90", first)
		}
		second := k.AccountContainerCPU(map[string]uint64{"app": 30, "side": 90}, "cgroup", 110, true, true)
		if second["app"] != 20 || second["side"] != 90 {
			t.Fatalf("second allocation = %v, want app=20 side=90", second)
		}
		failed := k.AccountContainerCPU(map[string]uint64{"app": 40, "side": 90}, "cgroup", 0, false, true)
		if failed["app"] != 20 || failed["side"] != 90 {
			t.Fatalf("invalid host read changed allocation: %v", failed)
		}
		recovered := k.AccountContainerCPU(map[string]uint64{"app": 40, "side": 90}, "cgroup", 120, true, true)
		if recovered["app"] != 30 || recovered["side"] != 90 || k.containerCPU.overhead != 0 {
			t.Fatalf("recovery allocation = %v, overhead=%d; want app=30 side=90 overhead=0", recovered, k.containerCPU.overhead)
		}
		stale := k.AccountContainerCPU(map[string]uint64{"app": 40, "side": 90}, "cgroup", 119, true, true)
		if stale["app"] != 30 || stale["side"] != 90 {
			t.Fatalf("stale host snapshot changed allocation: %v", stale)
		}
	})

	t.Run("retired container before first sample", func(t *testing.T) {
		k := &Kernel{}
		k.containerCPU.lifetime = map[string]uint64{"old": 50, "app": 50}
		k.RetireContainerCPU("old")
		got := k.AccountContainerCPU(map[string]uint64{"app": 50}, "cgroup", 100, true, true)
		if got["app"] != 50 || k.containerCPU.retiredAllocated != 50 {
			t.Fatalf("allocation = %v, retired=%d; want app=50 retired=50", got, k.containerCPU.retiredAllocated)
		}
		if _, ok := k.containerCPU.lifetime["old"]; ok {
			t.Fatal("retired container kept per-container lifetime state")
		}
	})

	t.Run("configured host read error freezes initial total", func(t *testing.T) {
		k := &Kernel{}
		first := k.AccountContainerCPU(map[string]uint64{"app": 30}, "cgroup", 0, false, true)
		if first["app"] != 0 {
			t.Fatalf("invalid initial host read allocated %v, want zero", first)
		}
		valid := k.AccountContainerCPU(map[string]uint64{"app": 30}, "cgroup", 10, true, true)
		if valid["app"] != 10 {
			t.Fatalf("first valid host allocated %v, want app=10", valid)
		}
	})

	t.Run("explicitly hostless sandbox follows guest deltas", func(t *testing.T) {
		k := &Kernel{}
		first := k.AccountContainerCPU(map[string]uint64{"app": 30}, "sentry", 0, false, false)
		second := k.AccountContainerCPU(map[string]uint64{"app": 40}, "sentry", 50, true, false)
		if first["app"] != 30 || second["app"] != 40 || k.containerCPU.overhead != 0 {
			t.Fatalf("hostless allocations = %v then %v, overhead=%d; want app=30 then 40 and no overhead", first, second, k.containerCPU.overhead)
		}
	})

	t.Run("successful zero host sample establishes host mode", func(t *testing.T) {
		k := &Kernel{}
		first := k.AccountContainerCPU(map[string]uint64{"app": 0}, "cgroup", 0, true, true)
		second := k.AccountContainerCPU(map[string]uint64{"app": 10}, "cgroup", 10, true, true)
		if first["app"] != 0 || second["app"] != 10 || k.containerCPU.overhead != 0 {
			t.Fatalf("zero-host allocations = %v then %v, overhead=%d; want 0 then 10 and no overhead", first, second, k.containerCPU.overhead)
		}
	})

	t.Run("guest growth waits for host growth", func(t *testing.T) {
		k := &Kernel{}
		k.AccountContainerCPU(map[string]uint64{"app": 10, "side": 90}, "cgroup", 100, true, true)
		unchangedHost := k.AccountContainerCPU(map[string]uint64{"app": 30, "side": 90}, "cgroup", 100, true, true)
		if unchangedHost["app"] != 10 || unchangedHost["side"] != 90 {
			t.Fatalf("unchanged host allocation = %v, want app=10 side=90", unchangedHost)
		}
		got := k.AccountContainerCPU(map[string]uint64{"app": 30, "side": 90}, "cgroup", 110, true, true)
		if got["app"] != 20 || got["side"] != 90 || k.containerCPU.overhead != 0 {
			t.Fatalf("delayed host allocation = %v, overhead=%d; want app=20 side=90 overhead=0", got, k.containerCPU.overhead)
		}
	})

	t.Run("initial zero guest usage remains overhead", func(t *testing.T) {
		k := &Kernel{}
		first := k.AccountContainerCPU(map[string]uint64{"app": 0}, "cgroup", 100, true, true)
		second := k.AccountContainerCPU(map[string]uint64{"app": 10}, "cgroup", 110, true, true)
		if first["app"] != 0 || second["app"] != 10 || k.containerCPU.overhead != 100 {
			t.Fatalf("allocations = %v then %v, overhead=%d; want 0 then 10 and overhead=100", first, second, k.containerCPU.overhead)
		}
	})

	t.Run("missing guest IDs remain overhead", func(t *testing.T) {
		k := &Kernel{}
		first := k.AccountContainerCPU(nil, "cgroup", 100, true, true)
		second := k.AccountContainerCPU(map[string]uint64{"app": 10}, "cgroup", 110, true, true)
		if len(first) != 0 || second["app"] != 10 || k.containerCPU.overhead != 100 {
			t.Fatalf("allocations = %v then %v, overhead=%d; want empty then app=10, overhead=100", first, second, k.containerCPU.overhead)
		}
	})

	t.Run("source switch with idle peer charges unmatched host to overhead", func(t *testing.T) {
		k := &Kernel{}
		k.AccountContainerCPU(map[string]uint64{"app": 10, "side": 90}, "cgroup", 100, true, true)
		switched := k.AccountContainerCPU(map[string]uint64{"app": 20, "side": 90}, "sentry", 110, true, true)
		idle := k.AccountContainerCPU(map[string]uint64{"app": 20, "side": 90}, "sentry", 120, true, true)
		active := k.AccountContainerCPU(map[string]uint64{"app": 30, "side": 90}, "sentry", 130, true, true)
		if switched["side"] != 90 || idle["side"] != 90 || active["side"] != 90 || active["app"] != 20 || k.containerCPU.overhead != 20 {
			t.Fatalf("switch=%v idle=%v active=%v overhead=%d; want side=90 app=20 overhead=20", switched, idle, active, k.containerCPU.overhead)
		}
	})

	t.Run("retirement after sample retains allocations", func(t *testing.T) {
		k := &Kernel{}
		k.AccountContainerCPU(map[string]uint64{"app": 50, "old": 50}, "cgroup", 100, true, true)
		k.RetireContainerCPU("old")
		got := k.AccountContainerCPU(map[string]uint64{"app": 60}, "cgroup", 110, true, true)
		if got["app"] != 60 || k.containerCPU.retiredAllocated != 50 || k.containerCPU.overhead != 0 {
			t.Fatalf("post-retirement allocation = %v, retired=%d overhead=%d; want app=60 retired=50 overhead=0", got, k.containerCPU.retiredAllocated, k.containerCPU.overhead)
		}
	})

	t.Run("restore remaps live container IDs", func(t *testing.T) {
		k := &Kernel{containerNames: map[string]string{"old": "workload"}}
		k.containerCPU.lifetime = map[string]uint64{"old": 30}
		k.AccountContainerCPU(map[string]uint64{"old": 30}, "cgroup", 60, true, true)
		k.RestoreContainerMapping(map[string]string{"workload": "new"})
		if got := k.ContainerCPUUsage()["new"]; got != 30 {
			t.Fatalf("restored container lifetime = %d, want 30", got)
		}
		if _, old := k.ContainerCPUUsage()["old"]; old {
			t.Fatal("old container ID retained after restore")
		}
		if got := k.containerCPU.allocated["new"]; got != 60 {
			t.Fatalf("restored host allocation = %d, want 60", got)
		}
	})

	t.Run("checkpoint persists allocation and guest baselines", func(t *testing.T) {
		k := &Kernel{}
		k.AccountContainerCPU(map[string]uint64{"app": 10, "side": 90}, "cgroup", 100, true, true)
		var buf bytes.Buffer
		if _, err := state.Save(context.Background(), &buf, &k.containerCPU); err != nil {
			t.Fatalf("saving CPU accounting: %v", err)
		}
		var restored containerCPUAccounting
		if _, err := state.Load(context.Background(), &buf, &restored); err != nil {
			t.Fatalf("loading CPU accounting: %v", err)
		}
		if restored.allocated["side"] != 90 || restored.guestPrev["app"] != 10 || restored.hostPrev != 100 {
			t.Fatalf("restored accounting = %+v, missing allocation or baselines", restored)
		}
	})

	t.Run("hostless restore retains pre-Event guest growth", func(t *testing.T) {
		k := &Kernel{}
		k.AccountContainerCPU(map[string]uint64{"old": 30}, "sentry", 0, false, false)
		var buf bytes.Buffer
		if _, err := state.Save(context.Background(), &buf, &k.containerCPU); err != nil {
			t.Fatalf("saving CPU accounting: %v", err)
		}
		restored := &Kernel{containerNames: map[string]string{"old": "workload"}}
		if _, err := state.Load(context.Background(), &buf, &restored.containerCPU); err != nil {
			t.Fatalf("loading CPU accounting: %v", err)
		}
		restored.afterLoad(context.Background())
		restored.RestoreContainerMapping(map[string]string{"workload": "new"})
		first := restored.AccountContainerCPU(map[string]uint64{"new": 40}, "sentry", 0, false, false)
		second := restored.AccountContainerCPU(map[string]uint64{"new": 50}, "sentry", 0, false, false)
		if first["new"] != 40 || second["new"] != 50 {
			t.Fatalf("restored guest-only allocations = %v then %v, want new=40 then 50", first, second)
		}
	})

	t.Run("host-backed restore to hostless retains comparable guest growth", func(t *testing.T) {
		k := &Kernel{}
		k.AccountContainerCPU(map[string]uint64{"old": 30}, "cgroup", 20, true, true)
		var buf bytes.Buffer
		if _, err := state.Save(context.Background(), &buf, &k.containerCPU); err != nil {
			t.Fatalf("saving CPU accounting: %v", err)
		}
		restored := &Kernel{containerNames: map[string]string{"old": "workload"}}
		if _, err := state.Load(context.Background(), &buf, &restored.containerCPU); err != nil {
			t.Fatalf("loading CPU accounting: %v", err)
		}
		restored.afterLoad(context.Background())
		restored.RestoreContainerMapping(map[string]string{"workload": "new"})
		first := restored.AccountContainerCPU(map[string]uint64{"new": 40}, "cgroup", 0, false, false)
		second := restored.AccountContainerCPU(map[string]uint64{"new": 50}, "cgroup", 0, false, false)
		if first["new"] != 30 || second["new"] != 40 {
			t.Fatalf("host-backed to hostless allocations = %v then %v, want new=30 then 40", first, second)
		}
	})

	t.Run("hostless restore rebases guest clock", func(t *testing.T) {
		k := &Kernel{}
		k.AccountContainerCPU(map[string]uint64{"app": 30}, "sentry", 0, false, false)
		k.afterLoad(context.Background())
		first := k.AccountContainerCPU(map[string]uint64{"app": 5}, "sentry", 0, false, false)
		second := k.AccountContainerCPU(map[string]uint64{"app": 15}, "sentry", 0, false, false)
		if first["app"] != 30 || second["app"] != 40 {
			t.Fatalf("restored guest-only allocations = %v then %v, want app=30 then 40", first, second)
		}
	})

	t.Run("restore can change from guest to host clock", func(t *testing.T) {
		k := &Kernel{}
		k.AccountContainerCPU(map[string]uint64{"app": 30}, "sentry", 0, false, false)
		k.afterLoad(context.Background())
		first := k.AccountContainerCPU(map[string]uint64{"app": 5}, "cgroup", 10, true, true)
		second := k.AccountContainerCPU(map[string]uint64{"app": 15}, "cgroup", 20, true, true)
		if first["app"] != 30 || second["app"] != 40 || k.containerCPU.overhead != 10 {
			t.Fatalf("restored guest-to-host allocations = %v then %v, overhead=%d; want app=30 then 40 overhead=10", first, second, k.containerCPU.overhead)
		}
	})

	t.Run("restore handles swapped IDs", func(t *testing.T) {
		k := &Kernel{containerNames: map[string]string{"a": "first", "b": "second"}}
		k.containerCPU.lifetime = map[string]uint64{"a": 10, "b": 20}
		k.RestoreContainerMapping(map[string]string{"first": "b", "second": "a"})
		got := k.ContainerCPUUsage()
		if got["a"] != 20 || got["b"] != 10 {
			t.Fatalf("swapped container lifetimes = %v, want a=20 b=10", got)
		}
	})

	t.Run("omitted idle ID keeps prior allocation", func(t *testing.T) {
		k := &Kernel{}
		k.AccountContainerCPU(map[string]uint64{"app": 10, "idle": 90}, "cgroup", 100, true, true)
		got := k.AccountContainerCPU(map[string]uint64{"app": 20}, "sentry", 110, true, true)
		if got["idle"] != 90 || got["app"] != 10 || k.containerCPU.overhead != 10 {
			t.Fatalf("allocation = %v, overhead=%d; want idle=90 app=10 overhead=10", got, k.containerCPU.overhead)
		}
	})

	t.Run("source switch and restore preserve allocations", func(t *testing.T) {
		k := &Kernel{}
		k.AccountContainerCPU(map[string]uint64{"app": 10}, "cgroup", 100, true, true)
		got := k.AccountContainerCPU(map[string]uint64{"app": 20}, "sentry", 120, true, true)
		if got["app"] != 100 || k.containerCPU.overhead != 20 {
			t.Fatalf("source switch = %v, overhead=%d; want app=100 overhead=20", got, k.containerCPU.overhead)
		}
		k.afterLoad(context.Background())
		got = k.AccountContainerCPU(map[string]uint64{"app": 30}, "sentry", 5, true, true)
		if got["app"] != 100 || k.containerCPU.overhead != 25 {
			t.Fatalf("restore = %v, overhead=%d; want app=100 overhead=25", got, k.containerCPU.overhead)
		}
	})
}
