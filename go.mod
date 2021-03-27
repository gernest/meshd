module github.com/gernest/meshd

go 1.16

require (
	github.com/cenkalti/backoff/v4 v4.1.0
	github.com/go-logr/logr v0.4.0
	github.com/go-logr/zapr v0.4.0
	github.com/google/uuid v1.2.0
	github.com/hashicorp/go-version v1.2.1
	github.com/miekg/dns v1.1.41
	github.com/servicemeshinterface/smi-sdk-go v0.5.0
	github.com/stretchr/testify v1.7.0
	go.uber.org/ratelimit v0.2.0
	go.uber.org/zap v1.16.0
	k8s.io/api v0.20.5
	k8s.io/apimachinery v0.20.5
	k8s.io/client-go v0.20.5
	sigs.k8s.io/controller-runtime v0.8.3
)
