package control

import (
	"context"
	goerrors "errors"
	"fmt"

	"github.com/gernest/meshd/pkg/annotations"
	"github.com/gernest/meshd/pkg/k8s"
	"github.com/gernest/meshd/pkg/portmapping"
	"github.com/gernest/meshd/pkg/shadow"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	table            *PortStateTable
}

func (c *Config) Table() *PortStateTable {
	if c.table != nil {
		return c.table
	}
	c.table = &PortStateTable{
		TCP: portmapping.NewPortMapping(c.MinTCPPort, c.MaxTCPPort),
		UDP: portmapping.NewPortMapping(c.MinUDPPort, c.MaxUDPPort),
	}
	return c.table
}

// Shadow implements reconciler loop that manages services/shadow services
type Shadow struct {
	client.Client
	Log logr.Logger
	s   *ShadowServiceManager
}

func New(cfg *Config, c client.Client, lg logr.Logger) *Shadow {
	return &Shadow{
		Client: c,
		Log:    lg,
		s: &ShadowServiceManager{
			table: cfg.Table(),
		},
	}
}

// Reconcile reconciles services with respective shadow services
func (r *Shadow) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	svc := &corev1.Service{}
	if err := r.Get(ctx, req.NamespacedName, svc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := r.Log.WithValues("Service", req.Name, "Namespace", req.Namespace)
	log.Info("Syncing service")
	trafficType, _ := annotations.GetTrafficType(svc.Annotations)
	shadowSvcName, _ := shadow.Name(req.Namespace, req.Name)
	shadowSvc := &corev1.Service{}
	shadowSvc.Name = shadowSvcName
	shadowSvc.Namespace = svc.Namespace
	if err := r.Get(ctx, req.NamespacedName, svc); err != nil {
		if !errors.IsNotFound(err) {
			return r.create(ctx, svc, shadowSvc, trafficType)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.update(ctx, svc, shadowSvc, trafficType)
}

func (r *Shadow) create(ctx context.Context, svc, shadowSvc *corev1.Service, trafficType string) (ctrl.Result, error) {
	ports := r.s.getServicePorts(svc, trafficType)
	if len(ports) == 0 {
		ports = []corev1.ServicePort{buildUnresolvablePort()}
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
	return ctrl.Result{}, r.Create(ctx, shadowSvc)
}

func (r *Shadow) update(ctx context.Context, svc, shadowSvc *corev1.Service, trafficType string) (ctrl.Result, error) {
	r.s.cleanupShadowServicePorts(svc, shadowSvc, trafficType)
	ports := r.s.getServicePorts(svc, trafficType)
	if len(ports) == 0 {
		ports = []corev1.ServicePort{buildUnresolvablePort()}
	}
	shadowSvc = shadowSvc.DeepCopy()
	shadowSvc.Spec.Ports = ports
	annotations.SetTrafficType(trafficType, shadowSvc.Annotations)
	return ctrl.Result{}, r.Update(ctx, shadowSvc)
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
			r.s.logger.Error(err, fmt.Sprintf("Unable to find traffic-type on shadow service %q", shadowSvc.Name))
			continue
		}

		if err != nil {
			r.s.logger.Error(err, fmt.Sprintf("Unable to load port mapping of shadow service %q: %v", shadowSvc.Name, err))
			continue
		}
		r.s.loadShadowServicePorts(&shadowSvc, trafficType)
	}
	return nil
}

func (r *Shadow) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Service{}).
		WithEventFilter(k8s.Filter()).
		Complete(r)
}
