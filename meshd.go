package meshd

import (
	"context"
	"reflect"
	"time"

	"github.com/gernest/meshd/pkg/topology"
	"github.com/go-logr/logr"
	"go.uber.org/atomic"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	access "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/access/v1alpha3"
	metrics "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/metrics/v1alpha2"
	specs "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/specs/v1alpha4"
	split "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/split/v1alpha4"
)

// Handler is a function that does something with the cluster topology.
type Handler func(*topology.Topology) error

type D struct {
	ctrl.Manager
	client   client.Client
	build    topology.Build
	handle   Handler
	topology *topology.Topology
	log      logr.Logger
	trigger  atomic.Int64
}

func (d *D) sync() {
	d.trigger.Inc()
}

func New(mgr ctrl.Manager, handle Handler) *D {
	return &D{
		Manager: mgr,
		client:  mgr.GetClient(),
		log:     ctrl.Log.WithName("meshd"),
		build:   topology.NewBuild(mgr.GetClient(), ctrl.Log.WithName("meshd.Topology")),
		handle:  handle,
	}
}

func (d *D) Start(ctx context.Context) error {
	if err := d.SetupWithManager(d.Manager); err != nil {
		return err
	}
	d.process(ctx)
	go d.start(ctx)
	return d.Manager.Start(ctx)
}

func (d *D) start(ctx context.Context) {
	ts := time.NewTicker(time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ts.C:
			count := d.trigger.Swap(0)
			if count > 0 {
				d.process(ctx)
			}
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
			if err := d.handle(b); err != nil {
				d.log.Error(err, "Failed to handle topology change")
			}
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

func (d *D) SetupWithManager(mgr ctrl.Manager) error {
	e := func(fn ...func(mgr ctrl.Manager) error) error {
		for _, f := range fn {
			if err := f(mgr); err != nil {
				return err
			}
		}
		return nil
	}
	return e(
		// watch for changes in the service mesh
		(&watch{fn: d.sync}).SetupWithManager,
	)
}
