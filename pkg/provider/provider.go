package provider

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/gernest/meshd/pkg/annotations"
	"github.com/gernest/meshd/pkg/topology"
	"github.com/gernest/tt/api"
	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
)

// MiddlewareBuilder is capable of building a middleware from service annotations.
type MiddlewareBuilder func(annotations map[string]string) (map[string]*api.Middleware, error)

// PortFinder finds service port mappings.
type PortFinder interface {
	Find(namespace, name string, port int32) (int32, bool)
}

// When multiple Traefik Routers listen to the same entrypoint and have the same Rule, the chosen router is the one
// with the highest priority. There are a few cases where this priority is crucial when building the dynamic configuration:
// - When a TrafficSplit is set on a k8s service, 2 Traefik Routers are created. One for accessing the k8s service
//   endpoints and one for accessing the services endpoints mentioned in the TrafficSplit. They both have the same Rule
//   but we should always prioritize the TrafficSplit. Therefore, TrafficSplit Routers should always have a higher priority.
// - When a TrafficTarget Destination targets pods of a k8s service and a TrafficSplit is set on this service. This
//   creates 2 Traefik Routers. One for the TrafficSplit and one for the TrafficTarget. We should always prioritize
//   TrafficSplits Routers and TrafficSplit Routers should always have a higher priority than TrafficTarget Routers.
const (
	priorityService = iota + 1
	priorityTrafficTargetDirect
	priorityTrafficTargetIndirect
	priorityTrafficSplit
)

// Config holds the Provider configuration.
type Config struct {
	ACL                bool
	DefaultTrafficType string
}

type Middleware struct{}
type Router struct {
	*api.Route
	EntryPoints []string
	Service     string
}
type Service struct{}

type APIConfig struct {
	HTTP struct {
		Middlewares map[string]*api.Middleware
		Routers     map[string]*Router
		Services    map[string][]*api.WeightedAddr
	}
	TCP struct {
		Middlewares map[string]*api.Middleware
		Routers     map[string]*Router
		Services    map[string][]*api.WeightedAddr
	}
	UDP struct {
		Middlewares map[string]*api.Middleware
		Routers     map[string]*Router
		Services    map[string][]*api.WeightedAddr
	}
}

// Provider holds the configuration for generating dynamic configuration from a kubernetes cluster state.
type Provider struct {
	config Config

	httpStateTable         PortFinder
	tcpStateTable          PortFinder
	udpStateTable          PortFinder
	buildServiceMiddleware MiddlewareBuilder

	Log logr.Logger
}

// New creates a new Provider.
func New(httpStateTable, tcpStateTable, udpStateTable PortFinder, middlewareBuilder MiddlewareBuilder, cfg Config, logger logr.Logger) *Provider {
	return &Provider{
		config:                 cfg,
		httpStateTable:         httpStateTable,
		tcpStateTable:          tcpStateTable,
		udpStateTable:          udpStateTable,
		Log:                    logger,
		buildServiceMiddleware: middlewareBuilder,
	}
}

// BuildConfig builds a dynamic configuration.
func (p *Provider) BuildConfig(t *topology.Topology) *APIConfig {
	cfg := &APIConfig{}
	cfg.HTTP.Middlewares = make(map[string]*api.Middleware)
	cfg.HTTP.Routers = make(map[string]*Router)
	cfg.HTTP.Services = make(map[string][]*api.WeightedAddr)

	cfg.TCP.Middlewares = make(map[string]*api.Middleware)
	cfg.TCP.Routers = make(map[string]*Router)
	cfg.TCP.Services = make(map[string][]*api.WeightedAddr)

	cfg.UDP.Middlewares = make(map[string]*api.Middleware)
	cfg.UDP.Routers = make(map[string]*Router)
	cfg.UDP.Services = make(map[string][]*api.WeightedAddr)

	for svcKey, svc := range t.Services {
		if err := p.buildConfigForService(t, cfg, svc); err != nil {
			err = fmt.Errorf("unable to build configuration: %w", err)
			svc.AddError(err)
			p.Log.Error(err, "Error building dynamic configuration for Service", "serviceKey", svcKey)
		}
	}

	return cfg
}

// buildConfigForService builds the dynamic configuration for the given service.
func (p *Provider) buildConfigForService(t *topology.Topology, cfg *APIConfig, svc *topology.Service) error {
	trafficType, err := annotations.GetTrafficType(svc.Annotations)
	if err != nil && !errors.Is(err, annotations.ErrNotFound) {
		return fmt.Errorf("unable to evaluate traffic-type annotation: %w", err)
	}

	if errors.Is(err, annotations.ErrNotFound) {
		trafficType = p.config.DefaultTrafficType
	}

	scheme, err := annotations.GetScheme(svc.Annotations)
	if err != nil {
		return fmt.Errorf("unable to evaluate scheme annotation: %w", err)
	}

	var middlewareKeys []string

	// Middlewares are currently supported only for HTTP services.
	if trafficType == annotations.ServiceTypeHTTP {
		middlewareKeys, err = p.buildMiddlewaresForConfigFromService(cfg, svc)
		if err != nil {
			return err
		}
	}

	// When ACL mode is on, all traffic must be forbidden unless explicitly authorized via a TrafficTarget.
	if p.config.ACL {
		p.buildACLConfigRoutersAndServices(t, cfg, svc, scheme, trafficType, middlewareKeys)
	} else if err = p.buildConfigRoutersAndServices(t, cfg, svc, scheme, trafficType, middlewareKeys); err != nil {
		return err
	}

	for _, tsKey := range svc.TrafficSplits {
		if err := p.buildServiceAndRoutersForTrafficSplit(t, cfg, tsKey, scheme, trafficType, middlewareKeys); err != nil {
			err = fmt.Errorf("unable to build routers and services : %w", err)
			t.TrafficSplits[tsKey].AddError(err)
			p.Log.Error(err, "Error building dynamic configuration for TrafficSplit", "key", tsKey)

			continue
		}
	}

	return nil
}

func (p *Provider) buildMiddlewaresForConfigFromService(cfg *APIConfig, svc *topology.Service) ([]string, error) {
	var middlewareKeys []string

	middlewares, err := p.buildServiceMiddleware(svc.Annotations)
	if err != nil {
		return middlewareKeys, fmt.Errorf("unable to build middlewares: %w", err)
	}

	for name, middleware := range middlewares {
		middlewareKey := getMiddlewareKey(svc, name)
		cfg.HTTP.Middlewares[middlewareKey] = middleware
		middlewareKeys = append(middlewareKeys, middlewareKey)
	}

	return middlewareKeys, nil
}

func (p *Provider) buildConfigRoutersAndServices(t *topology.Topology, cfg *APIConfig, svc *topology.Service, scheme, trafficType string, middlewareKeys []string) error {
	err := p.buildServicesAndRoutersForService(t, cfg, svc, scheme, trafficType, middlewareKeys)
	if err != nil {
		return fmt.Errorf("unable to build routers and services: %w", err)
	}

	return nil
}

func (p *Provider) buildACLConfigRoutersAndServices(t *topology.Topology, cfg *APIConfig, svc *topology.Service, scheme, trafficType string, middlewareKeys []string) {
	if trafficType == annotations.ServiceTypeHTTP {
		p.buildBlockAllRouters(cfg, svc)
	}

	for _, ttKey := range svc.TrafficTargets {
		if err := p.buildServicesAndRoutersForTrafficTarget(t, cfg, ttKey, scheme, trafficType, middlewareKeys); err != nil {
			err = fmt.Errorf("unable to build routers and services: %w", err)
			t.ServiceTrafficTargets[ttKey].AddError(err)
			p.Log.Error(err, "Error building dynamic configuration for TrafficTarget", "key", ttKey)
			continue
		}
	}
}

func (p *Provider) buildServicesAndRoutersForService(t *topology.Topology, cfg *APIConfig, svc *topology.Service, scheme, trafficType string, middlewares []string) error {
	svcKey := topology.Key{Name: svc.Name, Namespace: svc.Namespace}

	switch trafficType {
	case annotations.ServiceTypeHTTP:
		p.buildServicesAndRoutersForHTTPService(t, cfg, svc, scheme, middlewares, svcKey)

	case annotations.ServiceTypeTCP:
		p.buildServicesAndRoutersForTCPService(t, cfg, svc, svcKey)

	case annotations.ServiceTypeUDP:
		p.buildServicesAndRoutersForUDPService(t, cfg, svc, svcKey)

	default:
		return fmt.Errorf("unknown traffic-type %q", trafficType)
	}

	return nil
}

func (p *Provider) buildServicesAndRoutersForHTTPService(t *topology.Topology, cfg *APIConfig, svc *topology.Service, scheme string, middlewares []string, svcKey topology.Key) {
	httpRule := buildHTTPRuleFromService(svc)

	for _, svcPort := range svc.Ports {
		entrypoint, err := p.buildHTTPEntrypoint(svc, svcPort.Port)
		if err != nil {
			err = fmt.Errorf("unable to build HTTP entrypoint for port %d: %w", svcPort.Port, err)
			svc.AddError(err)
			p.Log.Error(err, "Error building dynamic configuration for Service", "key", svcKey)
			continue
		}

		key := getServiceRouterKeyFromService(svc, svcPort.Port)

		cfg.HTTP.Services[key] = p.buildHTTPServiceFromService(t, svc, scheme, svcPort)
		cfg.HTTP.Routers[key] = &Router{Route: buildHTTPRouter(httpRule, entrypoint, middlewares, key, priorityService)}
	}
}

func (p *Provider) buildServicesAndRoutersForTCPService(t *topology.Topology, cfg *APIConfig, svc *topology.Service, svcKey topology.Key) {
	rule := buildTCPRouterRule()

	for _, svcPort := range svc.Ports {
		entrypoint, err := p.buildTCPEntrypoint(svc, svcPort.Port)
		if err != nil {
			err = fmt.Errorf("unable to build TCP entrypoint for port %d: %w", svcPort.Port, err)
			svc.AddError(err)
			p.Log.Error(err, "Error building dynamic configuration for Service", "key", svcKey)
			continue
		}

		key := getServiceRouterKeyFromService(svc, svcPort.Port)

		addTCPService(cfg, key, p.buildTCPServiceFromService(t, svc, svcPort))
		addTCPRouter(cfg, key, buildTCPRouter(rule, entrypoint, key))
	}
}

func (p *Provider) buildServicesAndRoutersForUDPService(t *topology.Topology, cfg *APIConfig, svc *topology.Service, svcKey topology.Key) {
	for _, svcPort := range svc.Ports {
		entrypoint, err := p.buildUDPEntrypoint(svc, svcPort.Port)
		if err != nil {
			err = fmt.Errorf("unable to build UDP entrypoint for port %d: %w", svcPort.Port, err)
			svc.AddError(err)
			p.Log.Error(err, "Error building dynamic configuration for Service ", "key", svcKey)
			continue
		}

		key := getServiceRouterKeyFromService(svc, svcPort.Port)

		addUDPService(cfg, key, p.buildUDPServiceFromService(t, svc, svcPort))
		addUDPRouter(cfg, key, buildUDPRouter(entrypoint, key))
	}
}

func (p *Provider) buildServicesAndRoutersForTrafficTarget(t *topology.Topology, cfg *APIConfig, ttKey topology.ServiceTrafficTargetKey, scheme, trafficType string, middlewares []string) error {
	tt, ok := t.ServiceTrafficTargets[ttKey]
	if !ok {
		return fmt.Errorf("unable to find TrafficTarget %q", ttKey)
	}

	ttSvc, ok := t.Services[tt.Service]
	if !ok {
		return fmt.Errorf("unable to find Service %q", tt.Service)
	}

	switch trafficType {
	case annotations.ServiceTypeHTTP:
		p.buildHTTPServicesAndRoutersForTrafficTarget(t, tt, cfg, ttSvc, ttKey, scheme, middlewares)

	case annotations.ServiceTypeTCP:
		p.buildTCPServicesAndRoutersForTrafficTarget(t, tt, cfg, ttSvc, ttKey)
	default:
		return fmt.Errorf("unknown traffic-type %q", trafficType)
	}

	return nil
}

func (p *Provider) buildHTTPServicesAndRoutersForTrafficTarget(t *topology.Topology, tt *topology.ServiceTrafficTarget, cfg *APIConfig, ttSvc *topology.Service, ttKey topology.ServiceTrafficTargetKey, scheme string, middlewares []string) {
	if !hasTrafficTargetRuleHTTPRouteGroup(tt) {
		return
	}

	whitelistDirect := p.buildWhitelistMiddlewareFromTrafficTargetDirect(t, tt)
	whitelistDirectKey := getWhitelistMiddlewareKeyFromTrafficTargetDirect(tt)
	cfg.HTTP.Middlewares[whitelistDirectKey] = whitelistDirect

	rule := buildHTTPRuleFromTrafficTarget(tt, ttSvc)

	for _, svcPort := range tt.Destination.Ports {
		entrypoint, err := p.buildHTTPEntrypoint(ttSvc, svcPort.Port)
		if err != nil {
			err = fmt.Errorf("unable to build HTTP entrypoint for port %d: %w", svcPort.Port, err)
			tt.AddError(err)
			p.Log.Error(err, "Error building dynamic configuration for TrafficTarget", "key", ttKey)
			continue
		}

		svcKey := getServiceKeyFromTrafficTarget(tt, svcPort.Port)
		cfg.HTTP.Services[svcKey] = p.buildHTTPServiceFromTrafficTarget(t, tt, scheme, svcPort)

		rtrMiddlewares := addToSliceCopy(middlewares, whitelistDirectKey)

		directRtrKey := getRouterKeyFromTrafficTargetDirect(tt, svcPort.Port)
		cfg.HTTP.Routers[directRtrKey] = &Router{Route: buildHTTPRouter(rule, entrypoint, rtrMiddlewares, svcKey, priorityTrafficTargetDirect)}

		// If the ServiceTrafficTarget is the backend of at least one TrafficSplit we need an additional router with
		// a whitelist middleware which whitelists based on the X-Forwarded-For header instead of on the RemoteAddr value.
		if len(ttSvc.BackendOf) > 0 {
			whitelistIndirect := p.buildWhitelistMiddlewareFromTrafficTargetIndirect(t, tt)
			whitelistIndirectKey := getWhitelistMiddlewareKeyFromTrafficTargetIndirect(tt)
			cfg.HTTP.Middlewares[whitelistIndirectKey] = whitelistIndirect

			rule = buildHTTPRuleFromTrafficTargetIndirect(tt, ttSvc)
			rtrMiddlewares = addToSliceCopy(middlewares, whitelistIndirectKey)

			indirectRtrKey := getRouterKeyFromTrafficTargetIndirect(tt, svcPort.Port)
			cfg.HTTP.Routers[indirectRtrKey] = &Router{Route: buildHTTPRouter(rule, entrypoint, rtrMiddlewares, svcKey, priorityTrafficTargetIndirect)}
		}
	}
}

func (p *Provider) buildTCPServicesAndRoutersForTrafficTarget(t *topology.Topology, tt *topology.ServiceTrafficTarget, cfg *APIConfig, ttSvc *topology.Service, ttKey topology.ServiceTrafficTargetKey) {
	if !hasTrafficTargetRuleTCPRoute(tt) {
		return
	}

	rule := buildTCPRouterRule()

	for _, svcPort := range tt.Destination.Ports {
		entrypoint, err := p.buildTCPEntrypoint(ttSvc, svcPort.Port)
		if err != nil {
			err = fmt.Errorf("unable to build TCP entrypoint for port %d: %w", svcPort.Port, err)
			tt.AddError(err)
			p.Log.Error(err, "Error building dynamic configuration for TrafficTarget", "key", ttKey)
			continue
		}

		key := getServiceRouterKeyFromService(ttSvc, svcPort.Port)

		addTCPService(cfg, key, p.buildTCPServiceFromTrafficTarget(t, tt, svcPort))
		addTCPRouter(cfg, key, buildTCPRouter(rule, entrypoint, key))
	}
}

func (p *Provider) buildServiceAndRoutersForTrafficSplit(t *topology.Topology, cfg *APIConfig, tsKey topology.Key, scheme, trafficType string, middlewares []string) error {
	ts, ok := t.TrafficSplits[tsKey]
	if !ok {
		return fmt.Errorf("unable to find TrafficSplit %q", tsKey)
	}

	tsSvc, ok := t.Services[ts.Service]
	if !ok {
		return fmt.Errorf("unable to find Service %q", ts.Service)
	}

	switch trafficType {
	case annotations.ServiceTypeHTTP:
		p.buildHTTPServiceAndRoutersForTrafficSplit(t, cfg, tsKey, scheme, ts, tsSvc, middlewares)

	case annotations.ServiceTypeTCP:
		p.buildTCPServiceAndRoutersForTrafficSplit(cfg, tsKey, ts, tsSvc)

	case annotations.ServiceTypeUDP:
		p.buildUDPServiceAndRoutersForTrafficSplit(cfg, tsKey, ts, tsSvc)

	default:
		return fmt.Errorf("unknown traffic-type %q", trafficType)
	}

	return nil
}

func (p *Provider) buildHTTPServiceAndRoutersForTrafficSplit(t *topology.Topology, cfg *APIConfig, tsKey topology.Key, scheme string, ts *topology.TrafficSplit, tsSvc *topology.Service, middlewares []string) {
	rule := buildHTTPRuleFromTrafficSplit(ts, tsSvc)

	rtrMiddlewares := middlewares

	if p.config.ACL {
		whitelistDirect := p.buildWhitelistMiddlewareFromTrafficSplitDirect(t, ts)
		whitelistDirectKey := getWhitelistMiddlewareKeyFromTrafficSplitDirect(ts)
		cfg.HTTP.Middlewares[whitelistDirectKey] = whitelistDirect

		rtrMiddlewares = addToSliceCopy(middlewares, whitelistDirectKey)
	}

	for _, svcPort := range tsSvc.Ports {
		backendSvcs, err := p.buildServicesForTrafficSplitBackends(t, cfg, ts, svcPort, scheme)
		if err != nil {
			err = fmt.Errorf("unable to build HTTP backend services and port %d: %w", svcPort.Port, err)
			ts.AddError(err)
			p.Log.Error(err, "Error building dynamic configuration for TrafficSplit", "key", tsKey)
			continue
		}

		entrypoint, err := p.buildHTTPEntrypoint(tsSvc, svcPort.Port)
		if err != nil {
			err = fmt.Errorf("unable to build HTTP entrypoint for port %d: %w", svcPort.Port, err)
			ts.AddError(err)
			p.Log.Error(err, "Error building dynamic configuration for TrafficSplit", "key", tsKey)
			continue
		}

		svcKey := getServiceKeyFromTrafficSplit(ts, svcPort.Port)
		cfg.HTTP.Services[svcKey] = buildHTTPServiceFromTrafficSplit(backendSvcs)

		directRtrKey := getRouterKeyFromTrafficSplitDirect(ts, svcPort.Port)
		cfg.HTTP.Routers[directRtrKey] = &Router{Route: buildHTTPRouter(rule, entrypoint, rtrMiddlewares, svcKey, priorityTrafficSplit)}

		// If the ServiceTrafficSplit is a backend of at least one TrafficSplit we need an additional router with
		// a whitelist middleware which whitelists based on the X-Forwarded-For header instead of on the RemoteAddr value.
		if len(tsSvc.BackendOf) > 0 && p.config.ACL {
			whitelistIndirect := p.buildWhitelistMiddlewareFromTrafficSplitIndirect(t, ts)
			whitelistIndirectKey := getWhitelistMiddlewareKeyFromTrafficSplitIndirect(ts)
			cfg.HTTP.Middlewares[whitelistIndirectKey] = whitelistIndirect

			rule = buildHTTPRuleFromTrafficSplitIndirect(ts, tsSvc)
			rtrMiddlewaresindirect := addToSliceCopy(middlewares, whitelistIndirectKey)

			indirectRtrKey := getRouterKeyFromTrafficSplitIndirect(ts, svcPort.Port)
			cfg.HTTP.Routers[indirectRtrKey] = &Router{Route: buildHTTPRouter(rule, entrypoint, rtrMiddlewaresindirect, svcKey, priorityTrafficTargetIndirect)}
		}
	}
}

func (p *Provider) buildTCPServiceAndRoutersForTrafficSplit(cfg *APIConfig, tsKey topology.Key, ts *topology.TrafficSplit, tsSvc *topology.Service) {
	tcpRule := buildTCPRouterRule()

	for _, svcPort := range tsSvc.Ports {
		entrypoint, err := p.buildTCPEntrypoint(tsSvc, svcPort.Port)
		if err != nil {
			err = fmt.Errorf("unable to build TCP entrypoint for port %d: %w", svcPort.Port, err)
			ts.AddError(err)
			p.Log.Error(err, "Error building dynamic configuration for TrafficSplit", "key", tsKey)
			continue
		}

		backendSvcs := make([]*api.WeightedAddr, len(ts.Backends))

		for i, backend := range ts.Backends {
			backendSvcKey := getServiceKeyFromTrafficSplitBackend(ts, svcPort.Port, backend)

			addTCPService(cfg, backendSvcKey, buildTCPSplitTrafficBackendService(backend, svcPort.TargetPort.IntVal))
			backendSvcs[i] = &api.WeightedAddr{
				MetricLabels: map[string]string{
					"backendSvcKey": backendSvcKey,
				},
				Weight: int32(backend.Weight),
			}
		}

		key := getServiceRouterKeyFromService(tsSvc, svcPort.Port)

		addTCPService(cfg, key, buildTCPServiceFromTrafficSplit(backendSvcs))
		addTCPRouter(cfg, key, buildTCPRouter(tcpRule, entrypoint, key))
	}
}

func (p *Provider) buildUDPServiceAndRoutersForTrafficSplit(cfg *APIConfig, tsKey topology.Key, ts *topology.TrafficSplit, tsSvc *topology.Service) {
	for _, svcPort := range tsSvc.Ports {
		entrypoint, err := p.buildUDPEntrypoint(tsSvc, svcPort.Port)
		if err != nil {
			err = fmt.Errorf("unable to build UDP entrypoint for port %d: %w", svcPort.Port, err)
			ts.AddError(err)
			p.Log.Error(err, "Error building dynamic configuration for TrafficSplit", "key", tsKey)
			continue
		}

		backendSvcs := make([]*api.WeightedAddr, len(ts.Backends))

		for i, backend := range ts.Backends {
			backendSvcKey := getServiceKeyFromTrafficSplitBackend(ts, svcPort.Port, backend)

			addUDPService(cfg, backendSvcKey, buildUDPSplitTrafficBackendService(backend, svcPort.TargetPort.IntVal))
			backendSvcs[i] = &api.WeightedAddr{
				MetricLabels: map[string]string{
					"backendSvcKey": backendSvcKey,
				},
				Weight: int32(backend.Weight),
			}
		}

		key := getServiceRouterKeyFromService(tsSvc, svcPort.Port)

		addUDPService(cfg, key, buildUDPServiceFromTrafficSplit(backendSvcs))
		addUDPRouter(cfg, key, buildUDPRouter(entrypoint, key))
	}
}

func (p *Provider) buildServicesForTrafficSplitBackends(t *topology.Topology, cfg *APIConfig, ts *topology.TrafficSplit, svcPort corev1.ServicePort, scheme string) ([]*api.WeightedAddr, error) {
	backendSvcs := make([]*api.WeightedAddr, len(ts.Backends))

	for i, backend := range ts.Backends {
		backendSvc, ok := t.Services[backend.Service]
		if !ok {
			return nil, fmt.Errorf("unable to find Service %q", backend.Service)
		}

		if len(backendSvc.TrafficSplits) > 0 {
			tsKey := topology.Key{Name: ts.Name, Namespace: ts.Namespace}
			p.Log.Info("Nested TrafficSplits detected in TrafficSplit %q: Traefik Mesh doesn't support nested TrafficSplits", "key", tsKey)
		}

		backendSvcKey := getServiceKeyFromTrafficSplitBackend(ts, svcPort.Port, backend)

		cfg.HTTP.Services[backendSvcKey] = buildHTTPSplitTrafficBackendService(backend, scheme, svcPort.Port)
		backendSvcs[i] = &api.WeightedAddr{
			MetricLabels: map[string]string{
				"backendSvcKey": backendSvcKey,
			},
			Weight: int32(backend.Weight),
		}
	}

	return backendSvcs, nil
}

func (p *Provider) buildBlockAllRouters(cfg *APIConfig, svc *topology.Service) {
	rule := buildHTTPRuleFromService(svc)

	for _, svcPort := range svc.Ports {
		entrypoint, err := p.buildHTTPEntrypoint(svc, svcPort.Port)
		if err != nil {
			svcKey := topology.Key{Name: svc.Name, Namespace: svc.Namespace}
			err = fmt.Errorf("unable to build HTTP entrypoint for port %d: %w", svcPort.Port, err)
			svc.AddError(err)
			p.Log.Error(err, "Error building dynamic configuration for Service", "key", svcKey)
			continue
		}

		key := getServiceRouterKeyFromService(svc, svcPort.Port)
		cfg.HTTP.Routers[key] = &Router{
			EntryPoints: []string{entrypoint},
			// Middlewares: []string{blockAllMiddlewareKey},
			Service: blockAllServiceKey,
			Route: &api.Route{
				Rule:     rule,
				Priority: priorityService,
			},
		}
	}
}

func (p Provider) buildHTTPEntrypoint(svc *topology.Service, port int32) (string, error) {
	targetPort, ok := p.httpStateTable.Find(svc.Namespace, svc.Name, port)
	if !ok {
		return "", errors.New("port not found")
	}

	return fmt.Sprintf("http-%d", targetPort), nil
}

func (p Provider) buildTCPEntrypoint(svc *topology.Service, port int32) (string, error) {
	targetPort, ok := p.tcpStateTable.Find(svc.Namespace, svc.Name, port)
	if !ok {
		return "", errors.New("port not found")
	}

	return fmt.Sprintf("tcp-%d", targetPort), nil
}

func (p Provider) buildUDPEntrypoint(svc *topology.Service, port int32) (string, error) {
	targetPort, ok := p.udpStateTable.Find(svc.Namespace, svc.Name, port)
	if !ok {
		return "", errors.New("port not found")
	}

	return fmt.Sprintf("udp-%d", targetPort), nil
}

func (p *Provider) buildHTTPServiceFromService(t *topology.Topology, svc *topology.Service, scheme string, svcPort corev1.ServicePort) []*api.WeightedAddr {
	var servers []*api.WeightedAddr

	for _, podKey := range svc.Pods {
		pod, ok := t.Pods[podKey]
		if !ok {
			err := fmt.Errorf("Unable to find Pod %q for HTTP service from Service %s@%s", podKey, topology.Key{Name: svc.Name, Namespace: svc.Namespace})
			p.Log.Error(err, "Missing pod")
			continue
		}

		hostPort, ok := topology.ResolveServicePort(svcPort, pod.ContainerPorts)
		if !ok {
			p.Log.Info("Unable to resolve HTTP", "port", svcPort.Name, "pod", podKey)
			continue
		}

		address := net.JoinHostPort(pod.IP, strconv.Itoa(int(hostPort)))
		servers = append(servers, &api.WeightedAddr{
			Addr: &api.Address{
				Address: fmt.Sprintf("%s://%s", scheme, address),
			},
		})
	}
	return servers
}

func (p *Provider) buildHTTPServiceFromTrafficTarget(t *topology.Topology, tt *topology.ServiceTrafficTarget, scheme string, svcPort corev1.ServicePort) []*api.WeightedAddr {
	var servers []*api.WeightedAddr

	for _, podKey := range tt.Destination.Pods {
		pod, ok := t.Pods[podKey]
		if !ok {
			err := fmt.Errorf("Unable to find Pod %q for HTTP service from Traffic Target %q", podKey, topology.ServiceTrafficTargetKey{
				Service:       tt.Service,
				TrafficTarget: topology.Key{Name: tt.Name, Namespace: tt.Namespace},
			})
			p.Log.Error(err, "Missing pod")

			continue
		}

		hostPort, ok := topology.ResolveServicePort(svcPort, pod.ContainerPorts)
		if !ok {
			p.Log.Info("Unable to resolve HTTP service port for Pod ", "port", svcPort.TargetPort, "pod", podKey)
			continue
		}

		address := net.JoinHostPort(pod.IP, strconv.Itoa(int(hostPort)))

		servers = append(servers, &api.WeightedAddr{
			Addr: &api.Address{
				Address: fmt.Sprintf("%s://%s", scheme, address),
			},
		})
	}
	return servers
}

func (p *Provider) buildTCPServiceFromService(t *topology.Topology, svc *topology.Service, svcPort corev1.ServicePort) []*api.WeightedAddr {
	var servers []*api.WeightedAddr

	for _, podKey := range svc.Pods {
		pod, ok := t.Pods[podKey]
		if !ok {
			err := fmt.Errorf("Unable to find Pod %q for TCP service from Service %s@%s", podKey, topology.Key{Name: svc.Name, Namespace: svc.Namespace})
			p.Log.Error(err, "Missing pod")
			continue
		}

		hostPort, ok := topology.ResolveServicePort(svcPort, pod.ContainerPorts)
		if !ok {
			p.Log.Info("Unable to resolve TCP service port for Pod ", "port", svcPort.Name, "pod", podKey)
			continue
		}

		address := net.JoinHostPort(pod.IP, strconv.Itoa(int(hostPort)))
		servers = append(servers, &api.WeightedAddr{
			Addr: &api.Address{
				Address: address,
			},
		})
	}

	return servers
}

func (p *Provider) buildTCPServiceFromTrafficTarget(t *topology.Topology, tt *topology.ServiceTrafficTarget, svcPort corev1.ServicePort) []*api.WeightedAddr {
	var servers []*api.WeightedAddr

	for _, podKey := range tt.Destination.Pods {
		pod, ok := t.Pods[podKey]
		if !ok {
			err := fmt.Errorf("Unable to find Pod %q for TCP service from Traffic Target %s@%s", podKey, topology.Key{Name: tt.Name, Namespace: tt.Namespace})
			p.Log.Error(err, "Missing pod")
			continue
		}

		hostPort, ok := topology.ResolveServicePort(svcPort, pod.ContainerPorts)
		if !ok {
			p.Log.Info("Unable to resolve TCP service port  for Pod ", "port", svcPort.Name, "pod", podKey)
			continue
		}

		address := net.JoinHostPort(pod.IP, strconv.Itoa(int(hostPort)))

		servers = append(servers, &api.WeightedAddr{
			Addr: &api.Address{
				Address: address,
			},
		})
	}
	return servers
}

func (p *Provider) buildUDPServiceFromService(t *topology.Topology, svc *topology.Service, svcPort corev1.ServicePort) []*api.WeightedAddr {
	var servers []*api.WeightedAddr

	for _, podKey := range svc.Pods {
		pod, ok := t.Pods[podKey]
		if !ok {
			err := fmt.Errorf("Unable to find Pod %q for UDP service from Service %s@%s", podKey, topology.Key{Name: svc.Name, Namespace: svc.Namespace})
			p.Log.Error(err, "Missing pod")
			continue
		}

		hostPort, ok := topology.ResolveServicePort(svcPort, pod.ContainerPorts)
		if !ok {
			p.Log.Info("Unable to resolve UDP service port  for Pod ", "port", svcPort.Name, "pod", podKey)
			continue
		}

		address := net.JoinHostPort(pod.IP, strconv.Itoa(int(hostPort)))
		servers = append(servers, &api.WeightedAddr{
			Addr: &api.Address{
				Address: address,
			},
		})
	}

	return servers
}

// buildWhitelistMiddlewareFromTrafficTargetDirect builds an IPWhiteList middleware which blocks requests from
// unauthorized Pods. Authorized Pods are those listed in the ServiceTrafficTarget.Sources.
// This middleware doesn't work if used behind a proxy.
func (p *Provider) buildWhitelistMiddlewareFromTrafficTargetDirect(t *topology.Topology, tt *topology.ServiceTrafficTarget) *api.Middleware {
	// var IPs []string

	// for _, source := range tt.Sources {
	// 	for _, podKey := range source.Pods {
	// 		pod, ok := t.Pods[podKey]
	// 		if !ok {
	// 			p.logger.Errorf("Unable to find Pod %q for WhitelistMiddleware from Traffic Target %s@%s", podKey, topology.Key{Name: tt.Name, Namespace: tt.Namespace})
	// 			continue
	// 		}

	// 		IPs = append(IPs, pod.IP)
	// 	}
	// }

	// return &dynamic.Middleware{
	// 	IPWhiteList: &dynamic.IPWhiteList{
	// 		SourceRange: IPs,
	// 	},
	// }
	return &api.Middleware{}
}

// buildWhitelistMiddlewareFromTrafficSplitDirect builds an IPWhiteList middleware which blocks requests from
// unauthorized Pods. Authorized Pods are those that can access all the leaves of the TrafficSplit.
// This middleware doesn't work if used behind a proxy.
func (p *Provider) buildWhitelistMiddlewareFromTrafficSplitDirect(t *topology.Topology, ts *topology.TrafficSplit) *api.Middleware {
	// var IPs []string

	// for _, podKey := range ts.Incoming {
	// 	pod, ok := t.Pods[podKey]
	// 	if !ok {
	// 		p.logger.Errorf("Unable to find Pod %q for WhitelistMiddleware from Traffic Split %s@%s", podKey, topology.Key{Name: ts.Name, Namespace: ts.Namespace})
	// 		continue
	// 	}

	// 	IPs = append(IPs, pod.IP)
	// }

	// return &dynamic.Middleware{
	// 	IPWhiteList: &dynamic.IPWhiteList{
	// 		SourceRange: IPs,
	// 	},
	// }
	return &api.Middleware{}
}

// buildWhitelistMiddlewareFromTrafficTargetIndirect builds an IPWhiteList middleware which blocks requests from
// unauthorized Pods. Authorized Pods are those listed in the ServiceTrafficTarget.Sources.
// This middleware works only when used behind a proxy.
func (p *Provider) buildWhitelistMiddlewareFromTrafficTargetIndirect(t *topology.Topology, tt *topology.ServiceTrafficTarget) *api.Middleware {
	// whitelist := p.buildWhitelistMiddlewareFromTrafficTargetDirect(t, tt)
	// whitelist.IPWhiteList.IPStrategy = &dynamic.IPStrategy{
	// 	Depth: 1,
	// }

	// return whitelist
	return &api.Middleware{}
}

// buildWhitelistMiddlewareFromTrafficSplitIndirect builds an IPWhiteList middleware which blocks requests from
// unauthorized Pods. Authorized Pods are those that can access all the leaves of the TrafficSplit.
// This middleware works only when used behind a proxy.
func (p *Provider) buildWhitelistMiddlewareFromTrafficSplitIndirect(t *topology.Topology, ts *topology.TrafficSplit) *api.Middleware {
	// whitelist := p.buildWhitelistMiddlewareFromTrafficSplitDirect(t, ts)
	// whitelist.IPWhiteList.IPStrategy = &dynamic.IPStrategy{
	// 	Depth: 1,
	// }

	// return whitelist
	return &api.Middleware{}
}

func buildHTTPServiceFromTrafficSplit(backendSvc []*api.WeightedAddr) []*api.WeightedAddr {
	return backendSvc
}

func buildTCPServiceFromTrafficSplit(backendSvc []*api.WeightedAddr) []*api.WeightedAddr {
	return backendSvc
}

func buildUDPServiceFromTrafficSplit(backendSvc []*api.WeightedAddr) []*api.WeightedAddr {
	return backendSvc
}

func buildHTTPSplitTrafficBackendService(backend topology.TrafficSplitBackend, scheme string, port int32) []*api.WeightedAddr {

	return []*api.WeightedAddr{
		{
			Addr: &api.Address{
				Address: fmt.Sprintf("%s://%s.%s.traefik.mesh:%d", scheme, backend.Service.Name, backend.Service.Namespace, port),
			},
		},
	}
}

func buildTCPSplitTrafficBackendService(backend topology.TrafficSplitBackend, port int32) []*api.WeightedAddr {
	return []*api.WeightedAddr{
		{
			Addr: &api.Address{
				Address: fmt.Sprintf("%s.%s.traefik.mesh:%d", backend.Service.Name, backend.Service.Namespace, port),
			},
		},
	}
}

func buildUDPSplitTrafficBackendService(backend topology.TrafficSplitBackend, port int32) []*api.WeightedAddr {
	return []*api.WeightedAddr{
		{
			Addr: &api.Address{
				Address: fmt.Sprintf("%s.%s.traefik.mesh:%d", backend.Service.Name, backend.Service.Namespace, port),
			},
		},
	}
}

func buildHTTPRouter(routerRule *api.Rule, entrypoint string, middlewares []string, svcKey string, priority int) *api.Route {
	return &api.Route{
		// EntryPoints: []string{entrypoint},
		// Middlewares: middlewares,
		// Service:     svcKey,
		Rule:     routerRule,
		Priority: int32(getRulePriority(routerRule, priority)),
	}
}

func buildTCPRouter(routerRule *api.Rule, entrypoint string, svcKey string) *Router {
	return &Router{
		EntryPoints: []string{entrypoint},
		Route: &api.Route{
			Rule: routerRule,
		},
		Service: svcKey,
	}

}

func buildUDPRouter(entrypoint string, svcKey string) *Router {
	return &Router{
		EntryPoints: []string{entrypoint},
		Service:     svcKey,
	}
}

func hasTrafficTargetRuleTCPRoute(tt *topology.ServiceTrafficTarget) bool {
	for _, rule := range tt.Rules {
		if rule.TCPRoute != nil {
			return true
		}
	}

	return false
}

func hasTrafficTargetRuleHTTPRouteGroup(tt *topology.ServiceTrafficTarget) bool {
	for _, rule := range tt.Rules {
		if rule.HTTPRouteGroup != nil {
			return true
		}
	}

	return false
}

func addToSliceCopy(items []string, item string) []string {
	cpy := make([]string, len(items)+1)
	copy(cpy, items)
	cpy[len(items)] = item

	return cpy
}

func addTCPService(config *APIConfig, key string, service []*api.WeightedAddr) {
	config.TCP.Services[key] = service
}

func addTCPRouter(config *APIConfig, key string, router *Router) {
	config.TCP.Routers[key] = router
}

func addUDPService(config *APIConfig, key string, service []*api.WeightedAddr) {
	config.UDP.Services[key] = service
}

func addUDPRouter(config *APIConfig, key string, router *Router) {
	config.UDP.Routers[key] = router
}

func getBoolRef(v bool) *bool {
	return &v
}

func getIntRef(v int) *int {
	return &v
}
