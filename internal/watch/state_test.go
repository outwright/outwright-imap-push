// SPDX-FileCopyrightText: Outwright contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package watch

import "testing"

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := LoadState(dir, "home")
	if err != nil || st.UIDValidity != 0 || st.Last() != 0 {
		t.Fatalf("fresh state %+v %v", st, err)
	}
	st.set(7, 42)
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := LoadState(dir, "home")
	if err != nil || again.UIDValidity != 7 || again.Last() != 42 {
		t.Fatalf("reloaded %+v %v", again, err)
	}
	other, _ := LoadState(dir, "../other")
	if other.Last() != 0 {
		t.Fatal("names share a state file")
	}
}
