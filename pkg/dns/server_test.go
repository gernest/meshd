package dns

import (
	"testing"
	"time"

	"github.com/gernest/meshd/pkg/k8s"
	"github.com/gernest/meshd/pkg/zlg"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestServer(t *testing.T) {
	tests := []struct {
		desc      string
		domain    string
		expAnswer string
	}{
		{
			desc:      "should return an answer with the resolved IP",
			domain:    "whoami.default.dream.mesh.",
			expAnswer: "10.10.10.10",
		},
		{
			desc:   "should return an empty answer if IP cannot be resolved",
			domain: "whoami.foo.dream.mesh.",
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.desc, func(t *testing.T) {
			logger := zapr.NewLogger(zlg.Logger)

			serviceLister := newFakeK8sClient(t, &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "shadow-svc-247b8d4abd40affb14cc82edca56b2c7",
					Namespace: "dream-mesh",
					Labels: map[string]string{
						k8s.LabelServiceName:      "whoami",
						k8s.LabelServiceNamespace: "default",
					},
				},
				Spec: v1.ServiceSpec{
					ClusterIP: "10.10.10.10",
				},
			})

			resolver := NewShadowServiceResolver("dream.mesh", "dream-mesh", serviceLister)

			addr, server := newTestServer(logger, resolver)
			defer func() {
				require.NoError(t, server.Shutdown())
			}()

			msg := &dns.Msg{}
			msg.SetQuestion(test.domain, dns.TypeA)

			client := dns.Client{Timeout: 5 * time.Second}

			res, _, err := client.Exchange(msg, addr)
			require.NoError(t, err)

			if test.expAnswer == "" {
				require.Len(t, res.Answer, 0)
				return
			}

			require.Len(t, res.Answer, 1)
			assert.Equal(t, res.Answer[0].Header().Rrtype, dns.TypeA)
			assert.Equal(t, res.Answer[0].(*dns.A).A.String(), test.expAnswer)
		})
	}
}

func newTestServer(logger logr.Logger, resolver *ShadowServiceResolver) (string, *Server) {
	syncCh := make(chan struct{}, 1)

	server := NewServer(0, resolver, logger)
	server.NotifyStartedFunc = func() {
		syncCh <- struct{}{}
	}

	go func() {
		if err := server.ListenAndServe(); err != nil {
			logger.Error(err, "DNS server has stopped unexpectedly")
			syncCh <- struct{}{}
		}
	}()

	<-syncCh

	return server.Server.PacketConn.LocalAddr().String(), server
}
