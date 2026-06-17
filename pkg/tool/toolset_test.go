package tool

import (
	"context"
	"errors"
	"testing"
)

// staticSet is a fake ToolSet for testing RegisterSet.
type staticSet struct {
	tools []Tool
	err   error
}

func (s staticSet) Tools(context.Context) ([]Tool, error) { return s.tools, s.err }

func TestRegistry_RegisterSet(t *testing.T) {
	reg := NewRegistry()
	set := staticSet{tools: []Tool{echoTool("a"), echoTool("b")}}

	if err := reg.RegisterSet(context.Background(), set); err != nil {
		t.Fatalf("RegisterSet: %v", err)
	}
	for _, name := range []string{"a", "b"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("tool %q not registered from set", name)
		}
	}
}

func TestRegistry_RegisterSet_resolveError(t *testing.T) {
	reg := NewRegistry()
	wantErr := errors.New("discovery failed")
	if err := reg.RegisterSet(context.Background(), staticSet{err: wantErr}); !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

func TestRegistry_RegisterSet_duplicateName(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(echoTool("dup"))
	if err := reg.RegisterSet(context.Background(), staticSet{tools: []Tool{echoTool("dup")}}); err == nil {
		t.Error("expected duplicate-name error from RegisterSet")
	}
}

// compile-time: staticSet satisfies ToolSet.
var _ ToolSet = staticSet{}
