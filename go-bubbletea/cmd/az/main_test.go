package main

import (
	"reflect"
	"testing"
)

func TestProjectAliasArgs(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		args        []string
		wantArgs    []string
		wantAliased bool
	}{
		{
			name:        "add",
			command:     "add",
			args:        []string{"--name", "azedarach", "/tmp/azedarach"},
			wantArgs:    []string{"add", "--name", "azedarach", "/tmp/azedarach"},
			wantAliased: true,
		},
		{
			name:        "list",
			command:     "list",
			args:        []string{},
			wantArgs:    []string{"list"},
			wantAliased: true,
		},
		{
			name:        "remove",
			command:     "remove",
			args:        []string{"azedarach"},
			wantArgs:    []string{"remove", "azedarach"},
			wantAliased: true,
		},
		{
			name:        "switch",
			command:     "switch",
			args:        []string{"azedarach"},
			wantArgs:    []string{"switch", "azedarach"},
			wantAliased: true,
		},
		{
			name:        "non alias",
			command:     "issue",
			args:        []string{"list"},
			wantArgs:    nil,
			wantAliased: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs, gotAliased := projectAliasArgs(tc.command, tc.args)
			if gotAliased != tc.wantAliased {
				t.Fatalf("aliased = %v, want %v", gotAliased, tc.wantAliased)
			}
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Fatalf("args = %#v, want %#v", gotArgs, tc.wantArgs)
			}
		})
	}
}
