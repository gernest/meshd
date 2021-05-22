package k8s

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// ResourceFilter holds resource filtering rules.
type ResourceFilter struct {
	watchedNamespaces []string
	ignoredNamespaces []string
	ignoredServices   []namespaceName
	ignoredLabels     map[string]string
}

type namespaceName struct {
	Name      string
	Namespace string
}

// ResourceFilterOption adds a filtering rule to the given ResourceFilter.
type ResourceFilterOption func(filter *ResourceFilter)

// WatchNamespaces add the given namespaces to the list of namespaces to watch.
func WatchNamespaces(namespaces ...string) ResourceFilterOption {
	return func(filter *ResourceFilter) {
		filter.watchedNamespaces = append(filter.watchedNamespaces, namespaces...)
	}
}

// IgnoreNamespaces adds the given namespaces to the list of namespaces to ignore.
func IgnoreNamespaces(namespaces ...string) ResourceFilterOption {
	return func(filter *ResourceFilter) {
		filter.ignoredNamespaces = append(filter.ignoredNamespaces, namespaces...)
	}
}

// IgnoreLabel ignores resources with the given label and value.
func IgnoreLabel(name, value string) ResourceFilterOption {
	return func(filter *ResourceFilter) {
		filter.ignoredLabels[name] = value
	}
}

// IgnoreService adds the service to the list of service to ignore.
func IgnoreService(namespace, name string) ResourceFilterOption {
	return func(filter *ResourceFilter) {
		filter.ignoredServices = append(filter.ignoredServices, namespaceName{
			Namespace: namespace,
			Name:      name,
		})
	}
}

// NewResourceFilter creates a new ResourceFilter, configured with the given options.
func NewResourceFilter(opts ...ResourceFilterOption) *ResourceFilter {
	filter := &ResourceFilter{
		ignoredLabels: make(map[string]string),
	}

	for _, opt := range opts {
		opt(filter)
	}

	return filter
}

// IsIgnored returns true if the resource should be ignored.
func (f *ResourceFilter) IsIgnored(obj interface{}) bool {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return true
	}

	pMeta := meta.AsPartialObjectMetadata(accessor)

	// If we are not watching all namespaces, check if the namespace is in the watch list.
	if len(f.watchedNamespaces) > 0 && !contains(f.watchedNamespaces, pMeta.Namespace) {
		return true
	}

	// Check if the namespace is not explicitly ignored.
	if contains(f.ignoredNamespaces, pMeta.Namespace) {
		return true
	}

	// Check if the resource contains an ignored label.
	for ignoredName, ignoredValue := range f.ignoredLabels {
		if value, ok := pMeta.Labels[ignoredName]; ok && value == ignoredValue {
			return true
		}
	}

	if svc, ok := obj.(*corev1.Service); ok {
		// Check if the service is not explicitly ignored.
		if containsNamespaceName(f.ignoredServices, namespaceName{Namespace: svc.Namespace, Name: svc.Name}) {
			return true
		}

		// Ignore ExternalName services as they are not currently supported.
		if svc.Spec.Type == corev1.ServiceTypeExternalName {
			return true
		}
	}

	return false
}

func (f *ResourceFilter) List() *client.ListOptions {
	return &client.ListOptions{
		LabelSelector: f.Labels(),
		FieldSelector: f.Fields(),
	}
}

func (f *ResourceFilter) Labels() labels.Selector {
	if len(f.ignoredLabels) == 0 {
		return labels.Everything()
	}
	ls := labels.NewSelector()
	for k, v := range f.ignoredLabels {
		v, _ := labels.NewRequirement(k, selection.NotIn, []string{v})
		ls = ls.Add(*v)
	}
	return ls
}

func (f *ResourceFilter) labelRequirements() (o []*labels.Requirement) {
	for k, v := range f.ignoredLabels {
		v, _ := labels.NewRequirement(k, selection.NotIn, []string{v})
		o = append(o, v)
	}
	return
}

func (f *ResourceFilter) Fields() fields.Selector {
	var sl []fields.Selector
	for _, ns := range f.watchedNamespaces {
		sl = append(sl, fields.OneTermEqualSelector("metadata.namespace", ns))
	}
	for _, ns := range f.ignoredNamespaces {
		sl = append(sl, fields.OneTermNotEqualSelector("metadata.namespace", ns))
	}
	for _, ns := range f.ignoredServices {
		sl = append(sl, fields.OneTermNotEqualSelector("metadata.namespace", ns.Namespace))
		sl = append(sl, fields.OneTermNotEqualSelector("metadata.name", ns.Name))
		sl = append(sl, fields.OneTermNotEqualSelector("metadata.kind", "Service"))
	}
	if len(sl) == 0 {
		return fields.Everything()
	}
	return fields.AndSelectors(sl...)
}

func contains(slice []string, str string) bool {
	for _, item := range slice {
		if item == str {
			return true
		}
	}

	return false
}

func containsNamespaceName(slice []namespaceName, nn namespaceName) bool {
	for _, item := range slice {
		if item.Namespace == nn.Namespace && item.Name == nn.Name {
			return true
		}
	}

	return false
}

// Filter used to return watched resources
func Filter(fs *ResourceFilter) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(ce event.CreateEvent) bool {
			return !fs.IsIgnored(ce.Object)
		},
		UpdateFunc: func(ue event.UpdateEvent) bool {
			return !fs.IsIgnored(ue.ObjectNew)
		},
		DeleteFunc: func(de event.DeleteEvent) bool {
			return !fs.IsIgnored(de.Object)
		},
	}
}
