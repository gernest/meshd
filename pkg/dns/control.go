package dns

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Host this is the host name on which we are resolving all the services inside
// the mesh
const Host = "dream.mesh"

type Control struct {
	client.Client

	Log logr.Logger

	Namespace string
	Port      int32

	ServiceName string
	ServicePort int32
}

// Start starts the dns server
func (c *Control) Start(ctx context.Context) {
	resolver := &ShadowServiceResolver{
		namespace: c.Namespace,
		domain:    Host,
		serviceLister: func(ns, name string) (*v1.Service, error) {
			var svc corev1.Service
			err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &svc)
			if err != nil {
				return nil, err
			}
			return &svc, nil
		},
	}
	svr := NewServer(c.Port, resolver, c.Log)
	exitCtx, cancel := context.WithCancel(ctx)
	go func() {
		if err := svr.ListenAndServe(); err != nil {
			c.Log.Error(err, "Stopped dns server")
		}
		cancel()
	}()
	go func() {
		select {
		case <-exitCtx.Done():
		case <-ctx.Done():
			err := svr.Shutdown()
			if err != nil {
				c.Log.Error(err, "Failed to graceful shutdown")
			}
		}
	}()
}

// Configure patches dns service
func (c *Control) Configure(ctx context.Context) error {
	dnsClient := &Client{
		kubeClient: xclient{
			xdep: xdep{
				get: func(ctx context.Context, ns, name string) (*appsv1.Deployment, error) {
					var v appsv1.Deployment
					if err := c.Get(ctx, types.NamespacedName{}, &v); err != nil {
						return nil, err
					}
					return &v, nil
				},
				update: func(ctx context.Context, ns string, m *appsv1.Deployment) (*appsv1.Deployment, error) {
					if err := c.Update(ctx, m); err != nil {
						return nil, err
					}
					return m, nil
				},
			},
			xmap: xmap{
				get: func(ctx context.Context, ns, name string) (*corev1.ConfigMap, error) {
					var v corev1.ConfigMap
					if err := c.Get(ctx, types.NamespacedName{}, &v); err != nil {
						return nil, err
					}
					return &v, nil
				},
				update: func(ctx context.Context, ns string, m *corev1.ConfigMap) (*corev1.ConfigMap, error) {
					if err := c.Update(ctx, m); err != nil {
						return nil, err
					}
					return m, nil
				},
				create: func(ctx context.Context, ns string, m *corev1.ConfigMap) (*corev1.ConfigMap, error) {
					if err := c.Create(ctx, m); err != nil {
						return nil, err
					}
					return m, nil
				},
			},
			xsvc: xsvc{
				get: func(ctx context.Context, ns, name string) (*corev1.Service, error) {
					var v corev1.Service
					if err := c.Get(ctx, types.NamespacedName{}, &v); err != nil {
						return nil, err
					}
					return &v, nil
				},
			},
		},
		logger: c.Log,
	}

	dnsProvider, err := dnsClient.CheckDNSProvider(ctx)
	if err != nil {
		return fmt.Errorf("unable to find suitable DNS provider: %w", err)
	}

	switch dnsProvider {
	case CoreDNS:
		if err := dnsClient.ConfigureCoreDNS(ctx, c.Namespace, c.ServiceName, c.ServicePort); err != nil {
			return fmt.Errorf("unable to configure CoreDNS: %w", err)
		}

	case KubeDNS:
		if err := dnsClient.ConfigureKubeDNS(ctx, c.Namespace, c.ServiceName, c.ServicePort); err != nil {
			return fmt.Errorf("unable to configure KubeDNS: %w", err)
		}
	}
	return nil
}
