package conditions

import "testing"

func TestReferencesRoot(t *testing.T) {
	tests := []struct {
		name string
		cond *Condition
		root string
		want bool
	}{
		{name: "nil", cond: nil, root: "context", want: false},
		{name: "atomic context", cond: &Condition{Path: "context.role", Op: OpEq, Value: "admin"}, root: "context", want: true},
		{name: "atomic payload not context", cond: &Condition{Path: "payload.kind", Op: OpEq, Value: "v"}, root: "context", want: false},
		{name: "context exact root", cond: &Condition{Path: "context", Op: OpExists}, root: "context", want: true},
		{name: "payload prefix is not context", cond: &Condition{Path: "context_id.x", Op: OpExists}, root: "context", want: false},
		{
			name: "nested in any",
			cond: &Condition{Any: []Condition{
				{Path: "payload.a", Op: OpExists},
				{Path: "context.b", Op: OpExists},
			}},
			root: "context", want: true,
		},
		{
			name: "nested in not",
			cond: &Condition{Not: &Condition{Path: "context.b", Op: OpExists}},
			root: "context", want: true,
		},
		{
			name: "all branch no context",
			cond: &Condition{All: []Condition{
				{Path: "payload.a", Op: OpExists},
				{Path: "config.b", Op: OpExists},
			}},
			root: "context", want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReferencesRoot(tt.cond, tt.root); got != tt.want {
				t.Fatalf("ReferencesRoot = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	scope := Scope{
		Payload: map[string]any{"status": "error", "nested": map[string]any{"count": 2}},
		Context: map[string]any{"origin_user": "matt"},
		Config:  map[string]any{"enabled": true},
	}

	tests := []struct {
		name    string
		path    string
		present bool
		value   any
		wantErr bool
	}{
		{name: "payload nested", path: "payload.nested.count", present: true, value: 2},
		{name: "context value", path: "context.origin_user", present: true, value: "matt"},
		{name: "config value", path: "config.enabled", present: true, value: true},
		{name: "missing key", path: "payload.missing", present: false, value: nil},
		{name: "illegal root", path: "state.flag", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			present, value, err := ResolvePath(scope, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePath error = %v", err)
			}
			if present != tt.present {
				t.Fatalf("present = %v, want %v", present, tt.present)
			}
			if value != tt.value {
				t.Fatalf("value = %#v, want %#v", value, tt.value)
			}
		})
	}
}
