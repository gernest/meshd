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
	trafficType, err := annotations.GetTrafficType(svc.Annotations)
	if err != nil {
		return ctrl.Result{}, err
	}
	shadow := r.isShadow(svc)
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if svc.DeletionTimestamp.IsZero() {
			if shadow {
				return r.deleteShadow(svc, trafficType)
			}
			return r.delete(ctx, svc)
		}
		if shadow {
			controllerutil.AddFinalizer(svc, k8s.FinalService)
			return nil
		}
		if r.isCreated(svc) {
			return r.create(ctx, svc, trafficType)
		}
		return r.update(ctx, svc, trafficType)
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

func (r *Shadow) create(ctx context.Context, svc *corev1.Service, trafficType string) error {
	name, _ := shadow.Name(svc.Namespace, svc.Name)
	shadowSvc := &corev1.Service{}
	shadowSvc.Name = name
	shadowSvc.Namespace = svc.Namespace
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
	if err := r.Create(ctx, shadowSvc); err != nil {
		return err
	}
	controllerutil.AddFinalizer(svc, k8s.FinalService)
	return nil
}

func (r *Shadow) update(ctx context.Context, svc *corev1.Service, trafficType string) error {
	name, _ := shadow.Name(svc.Namespace, svc.Name)
	shadowSvc := &corev1.Service{}
	shadowSvc.Name = name
	shadowSvc.Namespace = svc.Namespace
	if err := r.Get(ctx, types.NamespacedName{}, shadowSvc); err != nil {
		return err
	}
	r.s.cleanupShadowServicePorts(svc, shadowSvc, trafficType)
	ports := r.s.getServicePorts(svc, trafficType)
	if len(ports) == 0 {
		ports = []corev1.ServicePort{buildUnresolvablePort()}
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

func (r *Shadow) deleteShadow(svc *corev1.Service, trafficType string) error {
	r.Log.Info("Deleting shadow service", "ServiceName", svc.Name)
	for _, sp := range svc.Spec.Ports {
		if err := r.s.unmapPort(svc.Namespace, svc.Name, trafficType, sp.Port); err != nil {
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
