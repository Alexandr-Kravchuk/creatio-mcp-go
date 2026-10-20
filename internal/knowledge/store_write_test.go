package knowledge

import (
	"context"
	"testing"
)

func TestManagerInstallsUpdatesAndDeletes(t *testing.T) {
	h := newConcurrencyHome(t)
	alias := "alpha"
	result, err := h.manager.Install(context.Background(), &alias)
	if err != nil || !result.Success || result.Sources[0].Status != "installed" {
		t.Fatalf("install: %+v %v", result, err)
	}
}
