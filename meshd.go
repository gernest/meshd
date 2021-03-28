package meshd

import (
	"context"
	"reflect"
	"time"

	"github.com/gernest/meshd/pkg/topology"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	access "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/access/v1alpha3"
	metrics "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/metrics/v1alpha2"
	specs "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/specs/v1alpha4"
	split "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/split/v1alpha4"
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

// AddToScheme adds all resources that this library manages to scheme
// - k8s.io/client-go/kubernetes/scheme
// - github.com/servicemeshinterface/smi-sdk-go/pkg/apis/access/v1alpha3
// - github.com/servicemeshinterface/smi-sdk-go/pkg/apis/specs/v1alpha4
// - github.com/servicemeshinterface/smi-sdk-go/pkg/apis/split/v1alpha4
// - github.com/servicemeshinterface/smi-sdk-go/pkg/apis/metrics/v1alpha2
func AddToScheme(scheme *runtime.Scheme) error {
	e := func(s *runtime.Scheme, fn ...func(*runtime.Scheme) error) error {
		for _, f := range fn {
			if err := f(s); err != nil {
				return err
			}
		}
		return nil
	}
	return e(scheme,
		clientgoscheme.AddToScheme,
		access.AddToScheme,
		specs.AddToScheme,
		split.AddToScheme,
		metrics.AddToScheme,
	)
}
