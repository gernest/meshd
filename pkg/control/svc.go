package control

import (
	"context"
	goerrors "errors"
	"reflect"

	"github.com/gernest/meshd/pkg/annotations"
	"github.com/gernest/meshd/pkg/k8s"
	"github.com/gernest/meshd/pkg/portmapping"
	"github.com/gernest/meshd/pkg/shadow"
	"github.com/gernest/meshd/pkg/topology"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type Config struct {
	ACLEnabled       bool
	DefaultMode      string
	Namespace        string
	WatchNamespaces  []string
	IgnoreNamespaces []string
	MinTCPPort       int32
	MaxTCPPort       int32
	MinUDPPort       int32
	MaxUDPPort       int32
	table            *shadow.PortStateTable
}

func (c *Config) Table() *shadow.PortStateTable {
	if c.table != nil {
		return c.table
	}
	c.table = &shadow.PortStateTable{
		TCP: portmapping.NewPortMapping(c.MinTCPPort, c.MaxTCPPort),
		UDP: portmapping.NewPortMapping(c.MinUDPPort, c.MaxUDPPort),
	}
	return c.table
}

type Handler func(*topology.Topology) error

// Shadow implements reconciler loop that manages services/shadow services
type Shadow struct {
	client.Client
	Log      logr.Logger
	manager  *shadow.Manager
	build    topology.Build
	handle   Handler
	topology *topology.Topology
}

func New(cfg *Config, c client.Client, lg logr.Logger) *Shadow {
	return &Shadow{
		Client: c,
		Log:    lg,
		manager: &shadow.Manager{
			Table: cfg.Table(),
		},
		build: topology.NewBuild(c, lg),
	}
}

func (r *Shadow) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if o, err := r.Sync(ctx, req); err != nil {
		return o, err
	}

	// build topology
	b, err := r.build.Build()
	if err != nil {
		r.Log.Error(err, "Failed to build topology")
		return ctrl.Result{}, err
	} else {
		if r.changed(b) {
			r.topology = b.DeepCopy()
			if err := r.handle(b); err != nil {
				r.Log.Error(err, "Failed to handle topology change")
				return ctrl.Result{}, err
			}
		}
	}
	return ctrl.Result{}, nil
}

func (r *Shadow) changed(n *topology.Topology) bool {
	if r.topology == nil {
		return true
	}
	// TODO find efficient comparison
	return reflect.DeepEqual(r.topology, n)
}

// Sync synchronizes the given service and its shadow service. If the shadow service doesn't exist it will be
// created. If it exists it will be updated and if the service doesn't exist, the shadow service will be removed.
func (r *Shadow) Sync(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	svc := &corev1.Service{}
	if err := r.Get(ctx, req.NamespacedName, svc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := r.Log.WithValues("Service", req.Name, "Namespace", req.Namespace)
	log.Info("Syncing service")
	trafficType, err := annotations.GetTrafficType(svc.Annotations)
	if err != nil {
		return ctrl.Result{}, err
	}
	shadow := r.isShadow(svc)
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if svc.DeletionTimestamp.IsZero() {
			if shadow {
				return r.deleteShadow(log, svc, trafficType)
			}
			return r.delete(ctx, svc)
		}
		if shadow {
			controllerutil.AddFinalizer(svc, k8s.FinalService)
			return nil
		}
		if r.isCreated(svc) {
			return r.create(ctx, log, svc, trafficType)
		}
		return r.update(ctx, log, svc, trafficType)
	})
	return ctrl.Result{}, err
}

func (r *Shadow) isCreated(svc *corev1.Service) bool {
	for _, x := range svc.Finalizers {
		if x == k8s.FinalService {
			return true
		}
	}
	return false
}

func (r *Shadow) isShadow(svc *corev1.Service) bool {
	return svc.Labels[k8s.LabelComponent] == k8s.ComponentShadowService
}

func (r *Shadow) create(ctx context.Context, log logr.Logger, svc *corev1.Service, trafficType string) error {
	name, _ := shadow.Name(svc.Namespace, svc.Name)
	shadowSvc := &corev1.Service{}
	shadowSvc.Name = name
	shadowSvc.Namespace = svc.Namespace
	ports := r.manager.GetServicePorts(log, svc, trafficType)
	if len(ports) == 0 {
		ports = []corev1.ServicePort{shadow.BuildUnresolvablePort()}
	}
	shadowSvcLabels := k8s.ShadowServiceLabels()
	shadowSvcLabels[k8s.LabelServiceNamespace] = svc.Namespace
	shadowSvcLabels[k8s.LabelServiceName] = svc.Name
	shadowSvc.Labels = shadowSvcLabels
	shadowSvc.Annotations = map[string]string{}
	shadowSvc.Spec = corev1.ServiceSpec{
		Selector: k8s.ProxyLabels(),
		Ports:    ports,
	}
	annotations.SetTrafficType(trafficType, shadowSvc.Annotations)
	if err := r.Create(ctx, shadowSvc); err != nil {
		return err
	}
	controllerutil.AddFinalizer(svc, k8s.FinalService)
	return nil
}

func (r *Shadow) update(ctx context.Context, log logr.Logger, svc *corev1.Service, trafficType string) error {
	name, _ := shadow.Name(svc.Namespace, svc.Name)
	shadowSvc := &corev1.Service{}
	shadowSvc.Name = name
	shadowSvc.Namespace = svc.Namespace
	if err := r.Get(ctx, types.NamespacedName{}, shadowSvc); err != nil {
		return err
	}
	r.manager.CleanupShadowServicePorts(log, svc, shadowSvc, trafficType)
	ports := r.manager.GetServicePorts(log, svc, trafficType)
	if len(ports) == 0 {
		ports = []corev1.ServicePort{shadow.BuildUnresolvablePort()}
	}
	shadowSvc = shadowSvc.DeepCopy()
	shadowSvc.Spec.Ports = ports
	annotations.SetTrafficType(trafficType, shadowSvc.Annotations)
	if err := r.Update(ctx, shadowSvc); err != nil {
		return err
	}
	return nil
}

func (r *Shadow) delete(ctx context.Context, svc *corev1.Service) error {
	r.Log.Info("Deleting service service")
	controllerutil.RemoveFinalizer(svc, k8s.FinalService)
	shadowSvcName, _ := shadow.Name(svc.Namespace, svc.Name)
	var shadow corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Namespace: svc.Namespace, Name: shadowSvcName}, &shadow); err != nil {
		return client.IgnoreNotFound(err)
	}
	err := r.Delete(ctx, &shadow)
	if err != nil {
		return err
	}
	r.Log.Info("Successfully deleted service")
	return nil
}

func (r *Shadow) deleteShadow(log logr.Logger, svc *corev1.Service, trafficType string) error {
	r.Log.Info("Deleting shadow service", "ServiceName", svc.Name)
	for _, sp := range svc.Spec.Ports {
		if err := r.manager.UnmapPort(log, svc.Namespace, svc.Name, trafficType, sp.Port); err != nil {
			r.Log.Error(err, "UUnable to unmap port",
				"ServiceName", svc.Name, "ServicePort", sp.Port, "Namespace", svc.Namespace)
			return err
		}
	}
	controllerutil.RemoveFinalizer(svc, k8s.FinalService)
	return nil
}

// Init initializes the shadowservice controller.
func (r *Shadow) Init(ctx context.Context) error {
	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: k8s.ShadowServiceLabels(),
	})
	if err != nil {
		return err
	}
	ls := &corev1.ServiceList{}
	err = r.List(ctx, ls, &client.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return err
	}
	for _, shadowSvc := range ls.Items {
		// If the traffic-type annotation has been manually removed we can't load its ports.
		trafficType, err := annotations.GetTrafficType(shadowSvc.Annotations)
		if goerrors.Is(err, annotations.ErrNotFound) {
			r.Log.Error(err, "Unable to find traffic-type on shadow service")
			continue
		}

		if err != nil {
			r.Log.Error(err, "Unable to load port mapping of shadow service")
			continue
		}
		r.manager.LoadShadowServicePorts(r.Log, &shadowSvc, trafficType)
	}
	return nil
}

func (r *Shadow) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Service{}).
		WithEventFilter(k8s.Filter()).
		Complete(r)
}
