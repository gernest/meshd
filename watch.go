package meshd

import (
	"context"

	"github.com/gernest/meshd/pkg/k8s"
	access "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/access/v1alpha2"
	specs "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/specs/v1alpha3"
	split "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/split/v1alpha3"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type watch struct {
	fn func()
}

func (r *watch) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.fn()
	return ctrl.Result{}, nil
}

// SetupWithManager creates a controller that builds cluster topology
func (r *watch) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&access.TrafficTarget{}).
		For(&specs.HTTPRouteGroup{}).
		For(&specs.TCPRoute{}).
		For(&split.TrafficSplit{}).
		For(&corev1.Service{}).
		For(&corev1.Pod{}).
		For(&corev1.Endpoints{}).
		WithEventFilter(r.predicate()).
		Complete(r)
}

func (r *watch) create(o client.Object) {}
func (r *watch) update(o client.Object) {}
func (r *watch) delete(o client.Object) {}

func (r *watch) predicate() predicate.Funcs {
	f := k8s.Filter()
	return predicate.Funcs{
		CreateFunc: func(ce event.CreateEvent) bool {
			ok := f.Create(ce)
			if ok {
				r.create(ce.Object)
			}
			return ok
		},
		UpdateFunc: func(ue event.UpdateEvent) bool {
			ok := f.Update(ue)
			if ok {
				r.update(ue.ObjectNew)
			}
			return ok
		},
		DeleteFunc: func(de event.DeleteEvent) bool {
			ok := f.Delete(de)
			if ok {
				r.delete(de.Object)
			}
			return ok
		},
	}
}
