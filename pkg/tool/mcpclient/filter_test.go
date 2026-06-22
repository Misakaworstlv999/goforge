package mcpclient

import (
	"context"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

func TestServerToolFilter(t *testing.T) {
	mk := func(name string) tool.Tool {
		return tool.NewTool(name, "d", func(context.Context, struct{}) (string, error) { return "", nil })
	}
	reg := tool.NewRegistry()
	if err := reg.Register(mk("km_corp_search"), mk("km_corp_get"), mk("github_issue"), mk("read_file")); err != nil {
		t.Fatal(err)
	}

	// "km-corp" config key → "km_corp_" prefix → both of that server's tools.
	sub := reg.Subset(ServerToolFilter("km-corp"))
	if len(sub.Schemas()) != 2 {
		t.Fatalf("ServerToolFilter(km-corp) selected %d tools, want 2", len(sub.Schemas()))
	}
	for _, s := range sub.Schemas() {
		if got, ok := sub.Get(s.Name); !ok || got.Name()[:8] != "km_corp_" {
			t.Errorf("unexpected tool in subset: %q", s.Name)
		}
	}
}
