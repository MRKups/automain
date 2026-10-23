package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/MRKups/automain/internal/app"
	"github.com/MRKups/automain/internal/cli"
	"github.com/MRKups/automain/internal/config"
	"github.com/MRKups/automain/internal/storage/sqlite"
	"github.com/MRKups/automain/web"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	baseCfg := config.FromEnv()
	factory := func(cmdCtx context.Context, cfg config.Config) (*app.Service, func(), error) {
		store, err := sqlite.Open(cmdCtx, cfg.DBPath)
		if err != nil {
			return nil, nil, err
		}
		cleanup := func() {
			if closeErr := store.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "error closing sqlite database: %v\n", closeErr)
			}
		}
		svc := app.NewService(cfg, app.Adapters{
			Store:      store,
			EmbeddedUI: web.HasIndex(),
		})
		return svc, cleanup, nil
	}

	code := cli.Execute(ctx, os.Args[1:], baseCfg, factory, os.Stdout, os.Stderr)
	os.Exit(code)
}
