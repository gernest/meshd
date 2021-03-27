package meshd

import (
	"context"
	"time"

	"github.com/gernest/meshd/pkg/topology"
	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type D struct {
	client   client.Client
	build    topology.Build
	handle   func(*topology.Topology)
	topology *topology.Topology
	log      logr.Logger
}

func (d *D) Start(ctx context.Context) {
	ts := time.NewTicker(time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ts.C:
			d.process(ctx)
		}
	}
}

func (d *D) process(ctx context.Context) {
	b, err := d.build.Build()
	if err != nil {
		d.log.Error(err, "Failed to build topology")
	} else {
		// check if topology has changed, keep a copy for next iteration
		d.handle(b)
	}
}
