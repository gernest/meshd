package control

import (
	"errors"
	"fmt"

	"github.com/gernest/meshd/pkg/annotations"
	"github.com/gernest/meshd/pkg/k8s"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// PortMapper is capable of storing and retrieving a port mapping for a given service.
type PortMapper interface {
	Find(namespace, name string, port int32) (int32, bool)
	Add(namespace, name string, port int32) (int32, error)
	Set(namespace, name string, port, targetPort int32) error
	Remove(namespace, name string, port int32) (int32, bool)
}

type PortStateTable struct {
	TCP PortMapper
	UDP PortMapper
}

// ShadowServiceManager manages shadow services.
type ShadowServiceManager struct {
	logger logr.Logger
	table  *PortStateTable
}

// getServicePorts returns the ports of the given user service, mapped with port opened on the proxy.
func (s *ShadowServiceManager) getServicePorts(svc *corev1.Service, trafficType string) []corev1.ServicePort {
	var ports []corev1.ServicePort

	for _, sp := range svc.Spec.Ports {
		if !isPortCompatible(trafficType, sp) {
			s.logger.Info("Unsupported port type %q on %q service %q in namespace %q, skipping port %d", sp.Protocol, trafficType, svc.Name, svc.Namespace, sp.Port)
			continue
		}

		targetPort, err := s.mapPort(svc.Name, svc.Namespace, trafficType, sp.Port)
		if err != nil {
			s.logger.Error(err, "Unable to map port", "Port", sp.Port)
			continue
		}

		ports = append(ports, corev1.ServicePort{
			Name:       sp.Name,
			Port:       sp.Port,
			Protocol:   sp.Protocol,
			TargetPort: intstr.FromInt(int(targetPort)),
		})
	}

	return ports
}

// cleanupShadowServicePorts unmap ports that have changed since the last update of the service.
func (s *ShadowServiceManager) cleanupShadowServicePorts(svc, shadowSvc *corev1.Service, trafficType string) {
	oldTrafficType, err := annotations.GetTrafficType(shadowSvc.Annotations)
	if errors.Is(err, annotations.ErrNotFound) {
		s.logger.Error(err, "Unable find traffic-type")
		return
	}

	if err != nil {
		s.logger.Error(err, "Unable to clean up ports ")
		return
	}

	var oldPorts []corev1.ServicePort

	// Release ports which have changed since the last update. This operation has to be done before mapping new
	// ports as the number of target ports available is limited.
	if oldTrafficType != trafficType {
		// All ports have to be released if the traffic-type has changed.
		oldPorts = shadowSvc.Spec.Ports
	} else {
		oldPorts = getRemovedOrUpdatedPorts(shadowSvc.Spec.Ports, svc.Spec.Ports)
	}

	for _, sp := range oldPorts {
		if err := s.unmapPort(svc.Namespace, svc.Name, oldTrafficType, sp.Port); err != nil {
			s.logger.Error(err, "Unable to unmap port", "Port", sp.Port)
		}
	}
}

// mapPort maps the given port to a port on the proxy, if not already done.
func (s *ShadowServiceManager) setPort(name, namespace, trafficType string, port, mappedPort int32) error {
	var stateTable PortMapper

	switch trafficType {
	case annotations.ServiceTypeTCP:
		stateTable = s.table.TCP
	case annotations.ServiceTypeUDP:
		stateTable = s.table.UDP
	default:
		return fmt.Errorf("unknown traffic type %q", trafficType)
	}

	if err := stateTable.Set(namespace, name, port, mappedPort); err != nil {
		return err
	}
	s.logger.Info("Loaded port", "Port", port, "MappedPort", mappedPort)
	return nil
}

// mapPort maps the given port to a port on the proxy, if not already done.
func (s *ShadowServiceManager) mapPort(name, namespace, trafficType string, port int32) (int32, error) {
	var stateTable PortMapper

	switch trafficType {
	case annotations.ServiceTypeTCP:
		stateTable = s.table.TCP
	case annotations.ServiceTypeUDP:
		stateTable = s.table.UDP
	default:
		return 0, fmt.Errorf("unknown traffic type %q", trafficType)
	}

	mappedPort, err := stateTable.Add(namespace, name, port)
	if err != nil {
		return 0, err
	}
	s.logger.Info("Mapped port", "Port", port, "MappedPort", mappedPort)
	return mappedPort, nil
}

// unmapPort releases the port on the proxy associated with the given port. This released port can then be
// remapped later on. Port releasing is delegated to the different port mappers, following the given traffic type.
func (s *ShadowServiceManager) unmapPort(namespace, name, trafficType string, port int32) error {
	var stateTable PortMapper

	switch trafficType {
	case annotations.ServiceTypeTCP:
		stateTable = s.table.TCP
	case annotations.ServiceTypeUDP:
		stateTable = s.table.UDP
	default:
		return fmt.Errorf("unknown traffic type %q", trafficType)
	}

	if mappedPort, ok := stateTable.Remove(namespace, name, port); ok {
		s.logger.Info("Unmapped port", "Port", port, "MappedPort", mappedPort)
	}

	return nil
}

// getRemovedOrUpdatedPorts returns the list of ports which have been removed or updated in the newPorts slice.
// New ports won't be returned.
func getRemovedOrUpdatedPorts(oldPorts, newPorts []corev1.ServicePort) []corev1.ServicePort {
	var ports []corev1.ServicePort

	for _, oldPort := range oldPorts {
		var found bool

		for _, newPort := range newPorts {
			if oldPort.Port == newPort.Port && oldPort.Protocol == newPort.Protocol {
				found = true

				break
			}
		}

		if !found {
			ports = append(ports, oldPort)
		}
	}

	return ports
}

// isPortCompatible checks if the given port is compatible with the given traffic type.
func isPortCompatible(trafficType string, sp corev1.ServicePort) bool {
	switch trafficType {
	case annotations.ServiceTypeUDP:
		return sp.Protocol == corev1.ProtocolUDP
	case annotations.ServiceTypeTCP:
		return sp.Protocol == corev1.ProtocolTCP
	default:
		return false
	}
}

// buildUnresolvablePort builds a service port with a fake port. This fake port can be used as a placeholder when a service
// doesn't have any compatible ports.
func buildUnresolvablePort() corev1.ServicePort {
	return corev1.ServicePort{
		Name:     "unresolvable-port",
		Protocol: corev1.ProtocolTCP,
		Port:     1666,
	}
}

// loadShadowServicePorts loads the port mapping of the given shadow service into the different port mappers.
func (s *ShadowServiceManager) loadShadowServicePorts(shadowSvc *corev1.Service, trafficType string) {
	namespace := shadowSvc.Labels[k8s.LabelServiceNamespace]
	name := shadowSvc.Labels[k8s.LabelServiceName]

	for _, sp := range shadowSvc.Spec.Ports {
		if !isPortCompatible(trafficType, sp) {
			s.logger.Info("Unsupported port type %q on %q service %q in namespace %q, skipping port %d", sp.Protocol, trafficType, shadowSvc.Name, shadowSvc.Namespace, sp.Port)
			continue
		}

		if err := s.setPort(name, namespace, trafficType, sp.Port, sp.TargetPort.IntVal); err != nil {
			s.logger.Error(err, fmt.Sprintf("Unable to load port %d for %q service %q in namespace %q: %v", sp.Port, trafficType, shadowSvc.Name, shadowSvc.Namespace, err))
			continue
		}
	}
}
