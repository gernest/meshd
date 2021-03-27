package meshd

import (
	"context"
	"reflect"
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

func New(c client.Client, log logr.Logger, handle func(*topology.Topology)) *D {
	return &D{
		client: c,
		log:    log,
		build:  topology.NewBuild(c, log),
		handle: handle,
	}
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
		if d.changed(b) {
			d.topology = b.DeepCopy()
			d.handle(b)
		}
	}
}

func (d *D) changed(n *topology.Topology) bool {
	if d.topology == nil {
		return true
	}
	// TODO find efficient comparison
	return reflect.DeepEqual(d.topology, n)
}
