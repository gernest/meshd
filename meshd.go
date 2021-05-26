package main

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/gernest/meshd/pkg/control"
)

// New initializes and registers the meshd reconciler on mgr.
func New(ctx context.Context, mgr ctrl.Manager, cfg *control.Config) error {
	svc := control.New(cfg, mgr.GetClient(), mgr.GetLogger())
	svc.SetupWithManager(mgr)
	// Load port mappings.
	if err := svc.Init(ctx); err != nil {
		return fmt.Errorf("could not load port mapper states: %w", err)
	}
	return nil
}
