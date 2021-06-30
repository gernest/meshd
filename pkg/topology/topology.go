package topology

import (
	v1 "github.com/gernest/meshd/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type Key = v1.Key
type Service = v1.Service
type Pod = v1.Pod
type ServiceTrafficTargetKey = v1.ServiceTrafficTargetKey
type ServiceTrafficTarget = v1.ServiceTrafficTarget
type TrafficSplit = v1.TrafficSplit
type TopologySpec = v1.TopologySpec
type ServiceTrafficTargetDestination = v1.ServiceTrafficTargetDestination
type ServiceTrafficTargetSource = v1.ServiceTrafficTargetSource
type TrafficSplitBackend = v1.TrafficSplitBackend
type TrafficSpec = v1.TrafficSpec

type Topology struct {
	Services              map[Key]*Service                                  `json:"services"`
	Pods                  map[Key]*Pod                                      `json:"pods"`
	ServiceTrafficTargets map[ServiceTrafficTargetKey]*ServiceTrafficTarget `json:"serviceTrafficTargets"`
	TrafficSplits         map[Key]*TrafficSplit                             `json:"trafficSplits"`
}

func (t *Topology) Spec() *TopologySpec {
	s := &TopologySpec{}
	for _, v := range t.Services {
		s.Services = append(s.Services, v)
	}
	for _, v := range t.Pods {
		s.Pods = append(s.Pods, v)
	}
	for _, v := range t.ServiceTrafficTargets {
		s.ServiceTrafficTargets = append(s.ServiceTrafficTargets, v)
	}
	for _, v := range t.TrafficSplits {
		s.TrafficSplits = append(s.TrafficSplits, v)
	}
	return s
}

func NewTopology() *Topology {
	return &Topology{
		Services:              make(map[Key]*Service),
		Pods:                  make(map[Key]*Pod),
		ServiceTrafficTargets: make(map[ServiceTrafficTargetKey]*ServiceTrafficTarget),
		TrafficSplits:         make(map[Key]*TrafficSplit),
	}
}

// ResolveServicePort resolves the given service port against the given container port list, as described in the
// Kubernetes documentation, and returns true if it has been successfully resolved, false otherwise.
//
// The Kubernetes documentation says: Port definitions in Pods have names, and you can reference these names in the
// targetPort attribute of a Service. This works even if there is a mixture of Pods in the Service using a single
// configured name, with the same network protocol available via different port numbers.
func ResolveServicePort(svcPort corev1.ServicePort, containerPorts []corev1.ContainerPort) (int32, bool) {
	if svcPort.TargetPort.Type == intstr.Int {
		return svcPort.TargetPort.IntVal, true
	}

	for _, containerPort := range containerPorts {
		if svcPort.TargetPort.StrVal == containerPort.Name && svcPort.Protocol == containerPort.Protocol {
			return containerPort.ContainerPort, true
		}
	}

	return 0, false
}
