package multiplexer

import (
	"errors"
	"testing"

	"github.com/eskelinenantti/zipln/internal/shell"
	"github.com/eskelinenantti/zipln/internal/shell/herdr"
	"github.com/eskelinenantti/zipln/internal/shell/rex"
	"github.com/eskelinenantti/zipln/internal/shell/tmux"
	"github.com/eskelinenantti/zipln/internal/test/mock"
	"github.com/eskelinenantti/zipln/internal/test/spy"
)

func TestSelect(t *testing.T) {
	for _, tt := range []struct {
		name, mux, herdr, rex, tmux, want string
	}{
		{"default", "auto", "", "", "", "tmux"},
		{"inside tmux", "auto", "", "", "tmux", "tmux"},
		{"inside herdr", "auto", "1", "", "", "herdr"},
		{"herdr inside tmux", "auto", "1", "", "outer-tmux", "herdr"},
		{"inside Rex", "auto", "", "session:1", "", "rex"},
		{"Rex inside tmux", "auto", "", "session:1", "outer-tmux", "rex"},
		{"herdr inside Rex", "auto", "1", "session:1", "", "herdr"},
		{"explicit tmux", "tmux", "1", "session:1", "", "tmux"},
		{"explicit herdr", "herdr", "", "session:1", "tmux", "herdr"},
		{"explicit Rex", "rex", "1", "", "tmux", "rex"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HERDR_ENV", tt.herdr)
			t.Setenv("REX_SESSION", tt.rex)
			t.Setenv("TMUX", tt.tmux)
			mux, err := Select(tt.mux, mock.Path{}, &spy.SpyRunner{})
			if err != nil {
				t.Fatal(err)
			}
			var got string
			switch mux.(type) {
			case herdr.Cmd:
				got = "herdr"
			case tmux.Cmd:
				got = "tmux"
			case rex.Cmd:
				got = "rex"
			}
			if got != tt.want {
				t.Fatalf("got %T, want %s", mux, tt.want)
			}
		})
	}
}

func TestInvalidOrMissingMultiplexer(t *testing.T) {
	for _, name := range []string{"tmux", "herdr", "rex"} {
		_, err := Select(name, mock.Path{Missing: []string{name}}, &spy.SpyRunner{})
		var missing shell.NotInstalledError
		if !errors.As(err, &missing) || missing.Cmd != name {
			t.Fatal(err)
		}
	}
	if _, err := Select("invalid", mock.Path{}, &spy.SpyRunner{}); err == nil {
		t.Fatal("expected invalid multiplexer error")
	}
}
