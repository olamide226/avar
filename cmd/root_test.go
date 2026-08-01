package cmd

import (
	"bytes"
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
)

func TestRootHelp_ListsImplementedPublicCommands_REQ_2_5(t *testing.T) {
	root := NewRootCommand("test", cli.Invocation{}, &App{})
	var out bytes.Buffer
	root.SetOut(&out)

	if err := root.Help(); err != nil {
		t.Fatalf("render root help: %v", err)
	}

	for _, want := range []string{
		"status", "stop [--all]", "snapshot [name]", "restore <name>",
		"reset [--yes]", "isolate [on|off [--yes]]",
		"destroy [--all|--orphaned] [--yes]", "code",
		"help [command]", "version", "forward host env var to a guest session",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("root help does not contain %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "internal idle-check") {
		t.Errorf("root help exposes the internal scheduler command:\n%s", out.String())
	}
}

func TestPublicCommandHelp_MatchesRegisteredPublicCommands(t *testing.T) {
	want := make([]string, 0, len(handlers)+2)
	for name := range handlers {
		if name != "internal" {
			want = append(want, name)
		}
	}
	want = append(want, "help", "version")
	sort.Strings(want)

	got := make([]string, 0, len(publicCommandHelp)+1)
	for name := range publicCommandHelp {
		got = append(got, name)
	}
	got = append(got, "help")
	sort.Strings(got)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("public help commands = %v, want implemented public commands %v", got, want)
	}
}

func TestHelpForInvocation_RoutesCommandHelpWithoutDispatch(t *testing.T) {
	tests := []struct {
		argv     []string
		wantName string
		wantHelp bool
	}{
		{argv: []string{"--help"}, wantHelp: true},
		{argv: []string{"help"}, wantHelp: true},
		{argv: []string{"help", "--help"}, wantHelp: true},
		{argv: []string{"help", "destroy"}, wantName: "destroy", wantHelp: true},
		{argv: []string{"destroy", "--help"}, wantName: "destroy", wantHelp: true},
		{argv: []string{"--arch", "amd64", "reset", "-h"}, wantName: "reset", wantHelp: true},
		{argv: []string{"status"}},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.argv, " "), func(t *testing.T) {
			inv, err := cli.Parse(tt.argv)
			if err != nil {
				t.Fatalf("parse invocation: %v", err)
			}
			gotName, gotHelp, err := helpForInvocation(inv)
			if err != nil {
				t.Fatalf("helpForInvocation(%q): %v", tt.argv, err)
			}
			if gotName != tt.wantName || gotHelp != tt.wantHelp {
				t.Errorf("helpForInvocation(%q) = (%q, %t), want (%q, %t)", tt.argv, gotName, gotHelp, tt.wantName, tt.wantHelp)
			}
		})
	}
}

func TestWriteCommandHelp_ListsOnlyImplementedFlags(t *testing.T) {
	var out bytes.Buffer
	if err := writeCommandHelp(&out, "destroy"); err != nil {
		t.Fatalf("write destroy help: %v", err)
	}
	for _, want := range []string{"--all", "--orphaned", "--yes", "confirmation"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("destroy help does not contain %q:\n%s", want, out.String())
		}
	}
	for _, absent := range []string{"--force", "--dry-run"} {
		if strings.Contains(out.String(), absent) {
			t.Errorf("destroy help advertises unsupported %q:\n%s", absent, out.String())
		}
	}
}

func TestHelpForInvocation_RejectsTooManyTopics(t *testing.T) {
	inv, err := cli.Parse([]string{"help", "stop", "status"})
	if err != nil {
		t.Fatalf("parse invocation: %v", err)
	}
	_, _, err = helpForInvocation(inv)
	if err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Errorf("helpForInvocation error = %v, want an arity error", err)
	}
}

func TestExecute_CommandHelpSkipsDispatch_REQ_2_5(t *testing.T) {
	original := handlers["destroy"]
	dispatched := false
	handlers["destroy"] = func(context.Context, *App, cli.Invocation) error {
		dispatched = true
		return nil
	}
	t.Cleanup(func() { handlers["destroy"] = original })

	if got := Execute(context.Background(), "test", []string{"destroy", "--help"}); got != 0 {
		t.Errorf("Execute(destroy --help) = %d, want 0", got)
	}
	if dispatched {
		t.Error("Execute(destroy --help) dispatched the command instead of rendering help")
	}
}
