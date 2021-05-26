package topology

import v1 "github.com/gernest/meshd/api/v1"

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
