module github.com/gernest/meshd

go 1.15

require (
	github.com/cenkalti/backoff/v4 v4.1.0
	github.com/gernest/tt v0.0.0-20210630152940-547147459e6d
	github.com/go-logr/logr v0.3.0
	github.com/go-logr/zapr v0.2.0
	github.com/google/uuid v1.1.2
	github.com/hashicorp/go-version v1.3.0
	github.com/miekg/dns v1.1.41
	github.com/servicemeshinterface/smi-sdk-go v0.5.0
	github.com/stretchr/testify v1.7.0
	go.uber.org/zap v1.16.0
	k8s.io/api v0.20.2
	k8s.io/apimachinery v0.20.2
	k8s.io/client-go v0.20.2
	sigs.k8s.io/controller-runtime v0.8.3
)
