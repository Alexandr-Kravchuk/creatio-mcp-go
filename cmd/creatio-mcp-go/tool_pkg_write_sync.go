package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

func init() {
	for _, item := range []struct {
		name string
		toDB bool
	}{{"pkg-to-db", true}, {"pkg-to-file-system", false}} {
		item := item
		pkgWriteCommandTool(item.name, toolAnnotations{Destructive: true},
			func(ctx context.Context, client *creatio.Client, _ map[string]any) (creatio.CommandResult, error) {
				return client.SyncPackages(ctx, item.toDB), nil
			}, func(args map[string]any) error {
				_, err := optionalStringArg(args, item.name, "environment-name")
				return err
			})
	}
}
