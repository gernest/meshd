package meshd

import (
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/gernest/meshd/pkg/control"
	access "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/access/v1alpha3"
	metrics "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/metrics/v1alpha2"
	specs "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/specs/v1alpha4"
	split "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/split/v1alpha4"
)

// New initializes and registers the meshd reconciler on mgr.
func New(mgr ctrl.Manager, cfg *control.Config) error {
	xscheme := mgr.GetScheme()
	if err := AddToScheme(xscheme); err != nil {
		return err
	}
	svc := control.New(cfg, mgr.GetClient(), mgr.GetLogger())
	svc.SetupWithManager(mgr)
	return nil
}

// AddToScheme adds all resources that this library manages to scheme
// - k8s.io/client-go/kubernetes/scheme
// - github.com/servicemeshinterface/smi-sdk-go/pkg/apis/access/v1alpha3
// - github.com/servicemeshinterface/smi-sdk-go/pkg/apis/specs/v1alpha4
// - github.com/servicemeshinterface/smi-sdk-go/pkg/apis/split/v1alpha4
// - github.com/servicemeshinterface/smi-sdk-go/pkg/apis/metrics/v1alpha2
func AddToScheme(scheme *runtime.Scheme) error {
	e := func(s *runtime.Scheme, fn ...func(*runtime.Scheme) error) error {
		for _, f := range fn {
			if err := f(s); err != nil {
				return err
			}
		}
		return nil
	}
	return e(scheme,
		clientgoscheme.AddToScheme,
		access.AddToScheme,
		specs.AddToScheme,
		split.AddToScheme,
		metrics.AddToScheme,
	)
}
