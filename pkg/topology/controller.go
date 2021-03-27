package topology

import (
	"context"

	"github.com/gernest/meshd/pkg/k8s"
	"github.com/go-logr/logr"
	access "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/access/v1alpha2"
	specs "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/specs/v1alpha3"
	split "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/split/v1alpha3"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Controller is the topology controller
type Controller struct {
	client.Client
	Handler func(*Topology)
	Log     logr.Logger
}

// Reconcile we don't do custom handling of resource here. We use this as a
// cheap mechanism to trigger topology updates when relevant resources changes
func (r *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	b := &builder{Client: r.Client, logger: r.Log}
	top, err := b.Build()
	if err != nil {
		r.Log.Error(err, "Topology build failure")
	} else {
		r.Handler(top)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager creates a controller that builds cluster topology
func (r *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&access.TrafficTarget{}).
		For(&specs.HTTPRouteGroup{}).
		For(&specs.TCPRoute{}).
		For(&split.TrafficSplit{}).
		For(&corev1.Service{}).
		For(&corev1.Pod{}).
		For(&corev1.Endpoints{}).
		WithEventFilter(k8s.Filter()).
		Complete(r)
}
