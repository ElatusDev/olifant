package challenge

import (
	"reflect"
	"testing"
)

func TestUnionWithUniversal(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "empty -> all scopes",
			in:   nil,
			want: []string{"universal", "backend", "webapp", "mobile", "e2e", "infra", "platform-process"},
		},
		{
			name: "empty slice -> all scopes",
			in:   []string{},
			want: []string{"universal", "backend", "webapp", "mobile", "e2e", "infra", "platform-process"},
		},
		{
			name: "single non-universal -> universal prefixed",
			in:   []string{"backend"},
			want: []string{"universal", "backend"},
		},
		{
			name: "multi non-universal -> universal prefixed, order preserved",
			in:   []string{"backend", "webapp"},
			want: []string{"universal", "backend", "webapp"},
		},
		{
			name: "already includes universal -> passthrough copy",
			in:   []string{"universal", "backend"},
			want: []string{"universal", "backend"},
		},
		{
			name: "universal not first -> passthrough copy preserves order",
			in:   []string{"backend", "universal", "webapp"},
			want: []string{"backend", "universal", "webapp"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := unionWithUniversal(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("unionWithUniversal(%v) = %v; want %v", tc.in, got, tc.want)
			}
			// Verify defensive copy — mutating result must not mutate input.
			if len(tc.in) > 0 && len(got) > 0 {
				got[0] = "MUTATED"
				if tc.in[0] == "MUTATED" {
					t.Errorf("unionWithUniversal shares backing slice with input")
				}
			}
		})
	}
}

func TestBuildV2ScopeWhere(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want map[string]interface{}
	}{
		{"empty -> nil", nil, nil},
		{"empty slice -> nil", []string{}, nil},
		{
			name: "single -> equality",
			in:   []string{"backend"},
			want: map[string]interface{}{"scope": "backend"},
		},
		{
			name: "multi -> $in",
			in:   []string{"universal", "backend"},
			want: map[string]interface{}{
				"scope": map[string]interface{}{"$in": []string{"universal", "backend"}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildV2ScopeWhere(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("buildV2ScopeWhere(%v) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}
