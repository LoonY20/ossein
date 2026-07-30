// Command crud runs the complete in-memory Ossein CRUD example.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	ossein "github.com/LoonY20/ossein"
)

type config struct {
	HTTP struct {
		Address string `env:"HTTP_ADDRESS" default:":8080"`
	}
}

func main() {
	settings, err := ossein.LoadConfig[config]()
	if err != nil {
		log.Fatal(err)
	}

	app, err := newApplication()
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.RunContext(ctx, settings.HTTP.Address); err != nil {
		log.Fatal(err)
	}
}
