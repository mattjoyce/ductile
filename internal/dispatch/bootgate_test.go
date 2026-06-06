package dispatch

import "testing"

func TestEvaluateBootGate(t *testing.T) {
	cases := []struct {
		name       string
		capability bool
		workers    bool
		override   bool
		wantMode   BootMode
		wantErr    bool
	}{
		{"capability + workers → enforce", true, true, false, BootEnforce, false},
		{"no capability + no workers → unconfined (dev)", false, false, false, BootUnconfined, false},
		{"capability + no workers → REFUSE", true, false, false, BootUnconfined, true},
		{"no capability + workers → REFUSE", false, true, false, BootUnconfined, true},
		{"override beats capability+workers (would-enforce)", true, true, true, BootUnconfined, false},
		{"override beats the capability+no-workers refuse", true, false, true, BootUnconfined, false},
		{"override beats the no-capability+workers refuse", false, true, true, BootUnconfined, false},
		{"override on a plain dev host is still unconfined", false, false, true, BootUnconfined, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, err := evaluateBootGate(tc.capability, tc.workers, tc.override)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if mode != tc.wantMode {
				t.Fatalf("mode = %v, want %v", mode, tc.wantMode)
			}
		})
	}
}
