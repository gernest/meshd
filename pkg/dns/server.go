package dns

import (
	"fmt"

	"github.com/go-logr/logr"
	"github.com/miekg/dns"
)

// dnsTTL tells the DNS resolver how long to cache a query before requesting a new one.
const dnsTTL = 60

// Server is a DNS server forwarding A requests to the configured resolver.
type Server struct {
	dns.Server

	resolver *ShadowServiceResolver
	logger   logr.Logger
}

// NewServer creates and returns a new DNS server.
func NewServer(port int32, resolver *ShadowServiceResolver, logger logr.Logger) *Server {
	mux := dns.NewServeMux()

	server := &Server{
		logger:   logger,
		resolver: resolver,
		Server: dns.Server{
			Addr:    fmt.Sprintf(":%d", port),
			Net:     "udp",
			Handler: mux,
		},
	}

	mux.HandleFunc(resolver.Domain(), server.handleDNSRequest)

	return server
}

func (s *Server) handleDNSRequest(w dns.ResponseWriter, r *dns.Msg) {
	msg := &dns.Msg{}

	msg.SetReply(r)
	msg.Authoritative = true

	for _, q := range r.Question {
		if q.Qtype != dns.TypeA {
			continue
		}

		ip, err := s.resolver.LookupFQDN(q.Name)
		if err != nil {
			s.logger.Error(err, "Unable to resolve", "name", q.Name)
			continue
		}

		rr, err := dns.NewRR(fmt.Sprintf("%s %d IN A %s", q.Name, dnsTTL, ip))
		if err != nil {
			s.logger.Error(err, "Unable to create RR", "name", q.Name)
			continue
		}

		msg.Answer = append(msg.Answer, rr)
	}

	if err := w.WriteMsg(msg); err != nil {
		s.logger.Error(err, "Unable to write DNS response:")
	}
}
