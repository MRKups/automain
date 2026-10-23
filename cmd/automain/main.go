package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/MRKups/automain/internal/app"
	"github.com/MRKups/automain/internal/cli"
	"github.com/MRKups/automain/internal/config"
	"github.com/MRKups/automain/web"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	baseCfg := config.FromEnv()
	factory := func(cfg config.Config) (*app.Service, func(), error) {
		svc := app.NewService(cfg, app.Adapters{
			EmbeddedUI: web.HasIndex(),
		})
		return svc, nil, nil
	}

	code := cli.Execute(ctx, os.Args[1:], baseCfg, factory, os.Stdout, os.Stderr)
	os.Exit(code)
}
