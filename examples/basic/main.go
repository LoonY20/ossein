// Command basic demonstrates a minimal Ossein HTTP application.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"

	ossein "github.com/LoonY20/ossein"
)

func main() {
	app := ossein.New()

	app.Get("/health", func(ctx *ossein.Context) error {
		return ctx.JSON(http.StatusOK, map[string]string{"status": "ok"})
	}).Named("health")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := app.RunContext(ctx, ":8080"); err != nil {
		log.Fatal(err)
	}
}
