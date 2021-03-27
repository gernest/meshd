package topology

import (
	"context"
	"fmt"

	mk8s "github.com/gernest/meshd/pkg/k8s"
	"github.com/go-logr/logr"
	access "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/access/v1alpha2"
	specs "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/specs/v1alpha3"
	split "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/split/v1alpha3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Build interface {
	Build() (*Topology, error)
}

// builder builds Topology objects based on the current state of a kubernetes cluster.
type builder struct {
	client.Client
	logger logr.Logger
}

func NewBuild(c client.Client, log logr.Logger) Build {
	return &builder{Client: c, logger: log}
}

// Build builds a graph representing the possible interactions between Pods and Services based on the current state
// of the kubernetes cluster.
func (b *builder) Build() (*Topology, error) {
	topology := NewTopology()

	res, err := b.loadResources()
	if err != nil {
		return nil, fmt.Errorf("unable to load resources: %w", err)
	}

	// Populate services.
	for _, svc := range res.Services {
		b.evaluateService(res, topology, svc)
	}

	// Populate services with traffic-split definitions.
	for _, ts := range res.TrafficSplits {
		b.evaluateTrafficSplit(res, topology, ts)
	}

	// Populate services with traffic-target definitions.
	for _, tt := range res.TrafficTargets {
		b.evaluateTrafficTarget(res, topology, tt)
	}

	b.populateTrafficSplitsAuthorizedIncomingTraffic(topology)

	return topology, nil
}

// evaluateService evaluates the given service. It adds the Service to the topology and its selected Pods.
func (b *builder) evaluateService(res *resources, topology *Topology, svc *corev1.Service) {
	svcKey := Key{svc.Name, svc.Namespace}

	svcPods := res.PodsBySvc[svcKey]
	pods := make([]Key, len(svcPods))

	for i, pod := range svcPods {
		pods[i] = getOrCreatePod(topology, pod)
	}

	topology.Services[svcKey] = &Service{
		Name:        svc.Name,
		Namespace:   svc.Namespace,
		Selector:    svc.Spec.Selector,
		Annotations: svc.Annotations,
		Ports:       svc.Spec.Ports,
		ClusterIP:   svc.Spec.ClusterIP,
		Pods:        pods,
	}
}

// evaluateTrafficTarget evaluates the given traffic-target. It adds a ServiceTrafficTargets on every Service which
// has pods with a service-account being the one defined in the traffic-target destination.
// When a ServiceTrafficTarget gets added to a Service, each source and destination pod will be added to the topology
// and linked to it.
func (b *builder) evaluateTrafficTarget(res *resources, topology *Topology, tt *access.TrafficTarget) {
	destSaKey := Key{tt.Spec.Destination.Name, tt.Spec.Destination.Namespace}

	sources := b.buildTrafficTargetSources(res, topology, tt)

	for svcKey, pods := range res.PodsBySvcBySa[destSaKey] {
		stt := &ServiceTrafficTarget{
			Name:      tt.Name,
			Namespace: tt.Namespace,
			Service:   svcKey,
			Sources:   sources,
		}

		svcTTKey := ServiceTrafficTargetKey{
			Service:       svcKey,
			TrafficTarget: Key{tt.Name, tt.Namespace},
		}
		topology.ServiceTrafficTargets[svcTTKey] = stt

		svc, ok := topology.Services[svcKey]
		if !ok {
			err := fmt.Errorf("unable to find Service %q", svcKey)
			stt.AddError(err)
			b.logger.Error(err, "Failed to build topology", "TrafficTarget", svcTTKey.TrafficTarget)
			continue
		}

		var err error

		stt.Rules, err = b.buildTrafficTargetRules(res, tt)
		if err != nil {
			err = fmt.Errorf("unable to build spec: %w", err)
			stt.AddError(err)
			b.logger.Error(err, "Failed to build topology", "TrafficTarget", svcTTKey.TrafficTarget)
			continue
		}

		stt.Destination, err = b.buildTrafficTargetDestination(topology, tt, pods, svc)
		if err != nil {
			stt.AddError(err)
			b.logger.Error(err, "Failed to build topology", "TrafficTarget", svcTTKey.TrafficTarget)
			continue
		}

		svc.TrafficTargets = append(svc.TrafficTargets, svcTTKey)

		// Add the ServiceTrafficTarget to the source and destination pods.
		addSourceAndDestinationToPods(topology, sources, svcTTKey)
	}
}

func (b *builder) buildTrafficTargetDestination(topology *Topology, tt *access.TrafficTarget, pods []*corev1.Pod, svc *Service) (ServiceTrafficTargetDestination, error) {
	dest := ServiceTrafficTargetDestination{
		ServiceAccount: tt.Spec.Destination.Name,
		Namespace:      tt.Spec.Destination.Namespace,
	}

	// Find out which are the destination pods.
	for _, pod := range pods {
		if pod.Status.PodIP == "" {
			continue
		}

		dest.Pods = append(dest.Pods, getOrCreatePod(topology, pod))
	}

	var err error

	// Find out which ports can be used on the destination service.
	dest.Ports, err = b.getTrafficTargetDestinationPorts(svc, tt)
	if err != nil {
		return dest, fmt.Errorf("unable to find destination ports on Service %q: %w", Key{Namespace: svc.Namespace, Name: svc.Name}, err)
	}

	return dest, nil
}

func addSourceAndDestinationToPods(topology *Topology, sources []ServiceTrafficTargetSource, svcTTKey ServiceTrafficTargetKey) {
	for _, source := range sources {
		for _, podKey := range source.Pods {
			// Skip pods which have not been added to the topology.
			if _, ok := topology.Pods[podKey]; !ok {
				continue
			}

			topology.Pods[podKey].SourceOf = append(topology.Pods[podKey].SourceOf, svcTTKey)
		}
	}

	for _, podKey := range topology.ServiceTrafficTargets[svcTTKey].Destination.Pods {
		// Skip pods which have not been added to the topology.
		if _, ok := topology.Pods[podKey]; !ok {
			continue
		}

		topology.Pods[podKey].DestinationOf = append(topology.Pods[podKey].DestinationOf, svcTTKey)
	}
}

// evaluateTrafficSplit evaluates the given traffic-split. If the traffic-split targets a known Service, a new TrafficSplit
// will be added to it. The TrafficSplit will be added only if all its backends expose the ports required by the Service.
func (b *builder) evaluateTrafficSplit(res *resources, topology *Topology, trafficSplit *split.TrafficSplit) {
	svcKey := Key{trafficSplit.Spec.Service, trafficSplit.Namespace}
	ts := &TrafficSplit{
		Name:      trafficSplit.Name,
		Namespace: trafficSplit.Namespace,
		Service:   svcKey,
	}

	tsKey := Key{trafficSplit.Name, trafficSplit.Namespace}
	topology.TrafficSplits[tsKey] = ts

	var err error

	ts.Rules, err = b.buildTrafficSplitSpecs(res, trafficSplit)
	if err != nil {
		err = fmt.Errorf("unable to build spec: %w", err)
		ts.AddError(err)
		b.logger.Error(err, "Failed to build topology", "TrafficTarget", tsKey)
		return
	}

	svc, ok := topology.Services[svcKey]
	if !ok {
		err := fmt.Errorf("unable to find root Service %q", svcKey)
		ts.AddError(err)
		b.logger.Error(err, "Failed to build topology", "TrafficTarget", tsKey)
		return
	}

	for _, backend := range trafficSplit.Spec.Backends {
		backendSvcKey := Key{backend.Service, trafficSplit.Namespace}

		backendSvc, ok := topology.Services[backendSvcKey]
		if !ok {
			err := fmt.Errorf("unable to find backend Service %q", backendSvcKey)
			ts.AddError(err)
			b.logger.Error(err, "Failed to build topology", "TrafficTarget", tsKey)
			continue
		}

		// As required by the SMI specification, backends must expose at least the same ports as the Service on
		// which the TrafficSplit is.
		if err := b.validateServiceAndBackendPorts(svc.Ports, backendSvc.Ports); err != nil {
			ts.AddError(err)
			b.logger.Error(err, "Failed to build topology ports mismatch",
				"TrafficTarget", tsKey,
				"Backend", backendSvcKey,
				"Service", svcKey,
			)
			continue
		}

		ts.Backends = append(ts.Backends, TrafficSplitBackend{
			Weight:  backend.Weight,
			Service: backendSvcKey,
		})

		backendSvc.BackendOf = append(backendSvc.BackendOf, tsKey)
	}

	svc.TrafficSplits = append(svc.TrafficSplits, tsKey)
}

func (b *builder) validateServiceAndBackendPorts(svcPorts []corev1.ServicePort, backendPorts []corev1.ServicePort) error {
	for _, svcPort := range svcPorts {
		var portFound bool

		for _, backendPort := range backendPorts {
			if svcPort.Port == backendPort.Port {
				portFound = true
				break
			}
		}

		if !portFound {
			return fmt.Errorf("port %d must be exposed", svcPort.Port)
		}
	}

	return nil
}

// populateTrafficSplitsAuthorizedIncomingTraffic computes the list of pods allowed to access a traffic-split. As
// traffic-splits may form a graph, it has to be done once all the traffic-splits have been processed. To avoid runtime
// issues, this method detects cycles in the graph.
func (b *builder) populateTrafficSplitsAuthorizedIncomingTraffic(topology *Topology) {
	loopCausingTrafficSplitsByService := make(map[*Service][]Key)

	for _, svc := range topology.Services {
		for _, tsKey := range svc.TrafficSplits {
			ts, ok := topology.TrafficSplits[tsKey]
			if !ok {
				b.logger.Info("Unable to find TrafficSplit", "TrafficTarget", tsKey)
				continue
			}

			pods, err := b.getIncomingPodsForTrafficSplit(topology, ts, map[Key]struct{}{})
			if err != nil {
				loopCausingTrafficSplitsByService[svc] = append(loopCausingTrafficSplitsByService[svc], tsKey)

				err = fmt.Errorf("unable to get incoming pods: %w", err)
				ts.AddError(err)
				b.logger.Error(err, "Failed to build topology", "TrafficTarget", tsKey)
				continue
			}

			ts.Incoming = pods
		}
	}

	// Remove the TrafficSplits that were detected to cause a loop.
	removeLoopCausingTrafficSplits(loopCausingTrafficSplitsByService)
}

func (b *builder) getIncomingPodsForTrafficSplit(topology *Topology, ts *TrafficSplit, visited map[Key]struct{}) ([]Key, error) {
	tsKey := Key{ts.Name, ts.Namespace}
	if _, ok := visited[tsKey]; ok {
		return nil, fmt.Errorf("circular reference detected on TrafficSplit %q in Service %q", tsKey, ts.Service)
	}

	visited[tsKey] = struct{}{}

	var union []Key

	for _, backend := range ts.Backends {
		backendPods, err := b.getIncomingPodsForService(topology, backend.Service, mapCopy(visited))
		if err != nil {
			return nil, err
		}

		union = unionPod(backendPods, union)

		if len(union) == 0 {
			return union, nil
		}
	}

	return union, nil
}

func (b *builder) getIncomingPodsForService(topology *Topology, svcKey Key, visited map[Key]struct{}) ([]Key, error) {
	var union []Key

	svc, ok := topology.Services[svcKey]
	if !ok {
		return nil, fmt.Errorf("unable to find Service %q", svcKey)
	}

	if len(svc.TrafficSplits) == 0 {
		return getPodsForServiceWithNoTrafficSplits(topology, svc)
	}

	for _, tsKey := range svc.TrafficSplits {
		ts, ok := topology.TrafficSplits[tsKey]
		if !ok {
			return nil, fmt.Errorf("unable to find TrafficSplit %q", tsKey)
		}

		tsPods, err := b.getIncomingPodsForTrafficSplit(topology, ts, visited)
		if err != nil {
			return nil, err
		}

		union = unionPod(tsPods, union)

		if len(union) == 0 {
			return union, nil
		}
	}

	return union, nil
}

func getPodsForServiceWithNoTrafficSplits(topology *Topology, svc *Service) ([]Key, error) {
	var pods []Key

	for _, ttKey := range svc.TrafficTargets {
		tt, ok := topology.ServiceTrafficTargets[ttKey]
		if !ok {
			return nil, fmt.Errorf("unable to find TrafficTarget %q", ttKey)
		}

		for _, source := range tt.Sources {
			pods = append(pods, source.Pods...)
		}
	}

	return pods, nil
}

// unionPod returns the union of the given two slices.
func unionPod(pods1, pods2 []Key) []Key {
	var union []Key

	if pods2 == nil {
		return pods1
	}

	p := map[Key]struct{}{}

	for _, pod := range pods1 {
		key := Key{pod.Name, pod.Namespace}

		p[key] = struct{}{}
	}

	for _, pod := range pods2 {
		key := Key{pod.Name, pod.Namespace}

		if _, ok := p[key]; ok {
			union = append(union, pod)
		}
	}

	return union
}

// buildTrafficTargetSources retrieves the Pod IPs for each Pod mentioned in a source of the given TrafficTarget.
// If a Pod IP is not yet available, the pod will be skipped.
func (b *builder) buildTrafficTargetSources(res *resources, t *Topology, tt *access.TrafficTarget) []ServiceTrafficTargetSource {
	sources := make([]ServiceTrafficTargetSource, len(tt.Spec.Sources))

	for i, source := range tt.Spec.Sources {
		srcSaKey := Key{source.Name, source.Namespace}

		pods := res.PodsByServiceAccounts[srcSaKey]

		var srcPods []Key

		for _, pod := range pods {
			if pod.Status.PodIP == "" {
				continue
			}

			srcPods = append(srcPods, getOrCreatePod(t, pod))
		}

		sources[i] = ServiceTrafficTargetSource{
			ServiceAccount: source.Name,
			Namespace:      source.Namespace,
			Pods:           srcPods,
		}
	}

	return sources
}

func (b *builder) buildTrafficTargetRules(res *resources, tt *access.TrafficTarget) ([]TrafficSpec, error) {
	var trafficSpecs []TrafficSpec

	for _, s := range tt.Spec.Rules {
		switch s.Kind {
		case mk8s.HTTPRouteGroupObjectKind:
			trafficSpec, err := b.buildHTTPRouteGroup(res.HTTPRouteGroups, tt.Namespace, s.Name, s.Matches)
			if err != nil {
				return nil, err
			}

			trafficSpecs = append(trafficSpecs, trafficSpec)
		case mk8s.TCPRouteObjectKind:
			trafficSpec, err := b.buildTCPRoute(res.TCPRoutes, tt.Namespace, s.Name)
			if err != nil {
				return nil, err
			}

			trafficSpecs = append(trafficSpecs, trafficSpec)
		default:
			return nil, fmt.Errorf("unknown spec type: %q", s.Kind)
		}
	}

	return trafficSpecs, nil
}

func (b *builder) buildTrafficSplitSpecs(res *resources, ts *split.TrafficSplit) ([]TrafficSpec, error) {
	var trafficSpecs []TrafficSpec

	for _, m := range ts.Spec.Matches {
		switch m.Kind {
		case mk8s.HTTPRouteGroupObjectKind:
			trafficSpec, err := b.buildHTTPRouteGroup(res.HTTPRouteGroups, ts.Namespace, m.Name, nil)
			if err != nil {
				return nil, err
			}

			trafficSpecs = append(trafficSpecs, trafficSpec)
		case mk8s.TCPRouteObjectKind:
			trafficSpec, err := b.buildTCPRoute(res.TCPRoutes, ts.Namespace, m.Name)
			if err != nil {
				return nil, err
			}

			trafficSpecs = append(trafficSpecs, trafficSpec)
		default:
			return nil, fmt.Errorf("unknown spec type: %q", m.Kind)
		}
	}

	return trafficSpecs, nil
}

func (b *builder) buildHTTPRouteGroup(httpRtGrps map[Key]*specs.HTTPRouteGroup, ns, name string, matches []string) (TrafficSpec, error) {
	key := Key{name, ns}

	httpRouteGroup, ok := httpRtGrps[key]
	if !ok {
		return TrafficSpec{}, fmt.Errorf("unable to find HTTPRouteGroup %q", key)
	}

	if len(matches) == 0 {
		return TrafficSpec{HTTPRouteGroup: httpRouteGroup}, nil
	}

	var httpMatches []specs.HTTPMatch

	// Copy HTTPRouteGroup and filter out matches that are not wanted.
	for _, matchName := range matches {
		var matchFound bool

		for _, match := range httpRouteGroup.Spec.Matches {
			matchFound = match.Name == matchName

			if matchFound {
				httpMatches = append(httpMatches, match)

				break
			}
		}

		if !matchFound {
			return TrafficSpec{}, fmt.Errorf("unable to find match %q in HTTPRouteGroup %q", name, key)
		}
	}

	httpRouteGroup = httpRouteGroup.DeepCopy()
	httpRouteGroup.Spec.Matches = httpMatches

	return TrafficSpec{HTTPRouteGroup: httpRouteGroup}, nil
}

func (b *builder) buildTCPRoute(tcpRts map[Key]*specs.TCPRoute, ns, name string) (TrafficSpec, error) {
	key := Key{name, ns}

	tcpRoute, ok := tcpRts[key]
	if !ok {
		return TrafficSpec{}, fmt.Errorf("unable to find TCPRoute %q", key)
	}

	return TrafficSpec{
		TCPRoute: tcpRoute,
	}, nil
}

// getTrafficTargetDestinationPorts gets the ports mentioned in the TrafficTarget.Destination.Port. If the destination
// port is defined but not on the service itself an error will be returned. If the destination port is not defined, the
// traffic allowed on all the service's ports.
func (b *builder) getTrafficTargetDestinationPorts(svc *Service, tt *access.TrafficTarget) ([]corev1.ServicePort, error) {
	port := tt.Spec.Destination.Port

	if port == nil {
		return svc.Ports, nil
	}

	key := Key{tt.Name, tt.Namespace}

	for _, svcPort := range svc.Ports {
		if svcPort.TargetPort.IntVal == int32(*port) {
			return []corev1.ServicePort{svcPort}, nil
		}
	}

	return nil, fmt.Errorf("destination port %d of TrafficTarget %q is not exposed by the service", *port, key)
}

func getOrCreatePod(topology *Topology, pod *corev1.Pod) Key {
	podKey := Key{pod.Name, pod.Namespace}

	if _, ok := topology.Pods[podKey]; !ok {
		var containerPorts []corev1.ContainerPort

		for _, container := range pod.Spec.Containers {
			containerPorts = append(containerPorts, container.Ports...)
		}

		topology.Pods[podKey] = &Pod{
			Name:            pod.Name,
			Namespace:       pod.Namespace,
			ServiceAccount:  pod.Spec.ServiceAccountName,
			OwnerReferences: pod.OwnerReferences,
			ContainerPorts:  containerPorts,
			IP:              pod.Status.PodIP,
		}
	}

	return podKey
}

func (b *builder) list(ls client.ObjectList) error {
	return b.List(context.TODO(), ls, &client.ListOptions{
		LabelSelector: labels.Everything(),
	})
}

func (b *builder) loadResources() (*resources, error) {
	res := &resources{
		Services:              make(map[Key]*corev1.Service),
		TrafficTargets:        make(map[Key]*access.TrafficTarget),
		TrafficSplits:         make(map[Key]*split.TrafficSplit),
		HTTPRouteGroups:       make(map[Key]*specs.HTTPRouteGroup),
		TCPRoutes:             make(map[Key]*specs.TCPRoute),
		PodsBySvc:             make(map[Key][]*corev1.Pod),
		PodsByServiceAccounts: make(map[Key][]*corev1.Pod),
		PodsBySvcBySa:         make(map[Key]map[Key][]*corev1.Pod),
	}

	err := b.loadServices(res)
	if err != nil {
		return nil, fmt.Errorf("unable to load Services: %w", err)
	}
	pods := &corev1.PodList{}
	if err := b.list(pods); err != nil {
		return nil, fmt.Errorf("unable to list Pods: %w", err)
	}

	eps := &corev1.EndpointsList{}
	if err := b.List(context.TODO(), eps, &client.ListOptions{LabelSelector: labels.Everything()}); err != nil {
		return nil, fmt.Errorf("unable to list Endpoints: %w", err)
	}
	tss := &split.TrafficSplitList{}
	if err := b.list(tss); err != nil {
		return nil, fmt.Errorf("unable to list TrafficSplits: %w", err)
	}

	httpRtGrps := &specs.HTTPRouteGroupList{}
	if err := b.list(httpRtGrps); err != nil {
		return nil, fmt.Errorf("unable to list HTTPRouteGroups: %w", err)
	}

	tcpRts := &specs.TCPRouteList{}
	if err := b.list(tcpRts); err != nil {
		return nil, fmt.Errorf("unable to list TCPRouteGroups: %w", err)
	}

	tts := &access.TrafficTargetList{}
	if err := b.list(tts); err != nil {
		return nil, fmt.Errorf("unable to list TrafficTargets: %w", err)
	}

	res.indexSMIResources(tts, tss, tcpRts, httpRtGrps)
	res.indexPods(pods, eps)

	return res, nil
}

func (b *builder) loadServices(res *resources) error {
	svcs := &corev1.ServiceList{}
	err := b.List(context.TODO(), svcs, &client.ListOptions{
		LabelSelector: labels.Everything(),
	})
	if err != nil {
		return fmt.Errorf("unable to list Services: %w", err)
	}
	for _, svc := range svcs.Items {
		res.Services[Key{svc.Name, svc.Namespace}] = &svc
	}
	return nil
}

type resources struct {
	Services        map[Key]*corev1.Service
	TrafficTargets  map[Key]*access.TrafficTarget
	TrafficSplits   map[Key]*split.TrafficSplit
	HTTPRouteGroups map[Key]*specs.HTTPRouteGroup
	TCPRoutes       map[Key]*specs.TCPRoute

	// Pods indexes.
	PodsBySvc             map[Key][]*corev1.Pod
	PodsByServiceAccounts map[Key][]*corev1.Pod
	PodsBySvcBySa         map[Key]map[Key][]*corev1.Pod
}

// indexPods populates the different pod indexes in the given resources object. It builds 3 indexes:
// - pods indexed by service-account
// - pods indexed by service
// - pods indexed by service indexed by service-account.
func (r *resources) indexPods(pods *corev1.PodList, eps *corev1.EndpointsList) {
	podsByName := make(map[Key]*corev1.Pod)

	r.indexPodsByServiceAccount(pods, podsByName)
	r.indexPodsByService(eps, podsByName)
}

func (r *resources) indexPodsByServiceAccount(pods *corev1.PodList, podsByName map[Key]*corev1.Pod) {
	for _, pod := range pods.Items {
		keyPod := Key{Name: pod.Name, Namespace: pod.Namespace}
		podsByName[keyPod] = &pod

		saKey := Key{pod.Spec.ServiceAccountName, pod.Namespace}
		r.PodsByServiceAccounts[saKey] = append(r.PodsByServiceAccounts[saKey], &pod)
	}
}

func (r *resources) indexPodsByService(eps *corev1.EndpointsList, podsByName map[Key]*corev1.Pod) {
	for _, ep := range eps.Items {
		// This map keeps track of service pods already indexed. A service pod can be listed in multiple endpoint
		// subset in function of the matched service ports.
		indexedServicePods := make(map[Key]struct{})

		for _, subset := range ep.Subsets {
			for _, address := range subset.Addresses {
				r.indexPodByService(&ep, address, podsByName, indexedServicePods)
			}
		}
	}
}

func (r *resources) indexPodByService(ep *corev1.Endpoints, address corev1.EndpointAddress, podsByName map[Key]*corev1.Pod, indexedServicePods map[Key]struct{}) {
	if address.TargetRef == nil {
		return
	}

	keyPod := Key{Name: address.TargetRef.Name, Namespace: address.TargetRef.Namespace}

	if _, exists := indexedServicePods[keyPod]; exists {
		return
	}

	pod, ok := podsByName[keyPod]
	if !ok {
		return
	}

	keySA := Key{Name: pod.Spec.ServiceAccountName, Namespace: pod.Namespace}
	keyEP := Key{Name: ep.Name, Namespace: ep.Namespace}

	if _, exists := r.PodsBySvcBySa[keySA]; !exists {
		r.PodsBySvcBySa[keySA] = make(map[Key][]*corev1.Pod)
	}

	r.PodsBySvcBySa[keySA][keyEP] = append(r.PodsBySvcBySa[keySA][keyEP], pod)
	r.PodsBySvc[keyEP] = append(r.PodsBySvc[keyEP], pod)

	indexedServicePods[keyPod] = struct{}{}
}

func (r *resources) indexSMIResources(tts *access.TrafficTargetList, tss *split.TrafficSplitList, tcpRts *specs.TCPRouteList, httpRtGrps *specs.HTTPRouteGroupList) {
	for _, httpRouteGroup := range httpRtGrps.Items {
		key := Key{httpRouteGroup.Name, httpRouteGroup.Namespace}
		r.HTTPRouteGroups[key] = &httpRouteGroup
	}

	for _, tcpRoute := range tcpRts.Items {
		key := Key{tcpRoute.Name, tcpRoute.Namespace}
		r.TCPRoutes[key] = &tcpRoute
	}

	for _, trafficTarget := range tts.Items {
		// If the destination namespace is empty or blank, set it to the trafficTarget namespace.
		if trafficTarget.Spec.Destination.Namespace == "" {
			trafficTarget.Spec.Destination.Namespace = trafficTarget.Namespace
		}

		key := Key{trafficTarget.Name, trafficTarget.Namespace}
		r.TrafficTargets[key] = &trafficTarget
	}

	for _, trafficSplit := range tss.Items {
		key := Key{trafficSplit.Name, trafficSplit.Namespace}
		r.TrafficSplits[key] = &trafficSplit
	}
}

func mapCopy(m map[Key]struct{}) map[Key]struct{} {
	cpy := map[Key]struct{}{}

	for k, b := range m {
		cpy[k] = b
	}

	return cpy
}

func removeLoopCausingTrafficSplits(loopCausingTrafficSplitsByService map[*Service][]Key) {
	for svc, tss := range loopCausingTrafficSplitsByService {
		for _, loopTS := range tss {
			for i, ts := range svc.TrafficSplits {
				if ts == loopTS {
					svc.TrafficSplits = append(svc.TrafficSplits[:i], svc.TrafficSplits[i+1:]...)
					break
				}
			}
		}
	}
}
