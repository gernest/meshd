package provider

import (
	"fmt"
	"strings"

	"github.com/gernest/meshd/pkg/topology"
	"github.com/gernest/tt/api"
	specs "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/specs/v1alpha3"
)

const subdomain = "dream.mesh"

func buildHTTPRuleFromTrafficSpecs(specs []topology.TrafficSpec) *api.Rule {
	var orRules []*api.Rule

	for _, spec := range specs {
		if spec.HTTPRouteGroup == nil {
			continue
		}

		for _, match := range spec.HTTPRouteGroup.Spec.Matches {
			var matchParts []*api.Rule

			// Handle Path filtering.
			matchParts = appendPathFilter(matchParts, match)

			// Handle Method filtering.
			matchParts = appendMethodFilter(matchParts, match)

			// // Handle Header filtering.
			matchParts = appendHeaderFilter(matchParts, match)

			// Conditions within a HTTPMatch must all be fulfilled to be considered valid.
			if len(matchParts) > 0 {
				orRules = append(orRules, &api.Rule{
					Match: &api.Rule_All{
						All: &api.Rule_List{Rules: matchParts},
					},
				})
			}
		}
	}
	if len(orRules) == 0 {
		return nil
	}
	// At least one HTTPMatch in the Specs must be valid.
	return &api.Rule{
		Match: &api.Rule_Any{
			Any: &api.Rule_List{Rules: orRules},
		},
	}
}

func appendPathFilter(matchParts []*api.Rule, match specs.HTTPMatch) []*api.Rule {
	if match.PathRegex == "" {
		return matchParts
	}
	// return append(matchParts, fmt.Sprintf("PathPrefix(`/{path:%s}`)", pathRegex))
	return append(matchParts, &api.Rule{
		Match: &api.Rule_Http{
			Http: &api.Rule_HTTP{
				Match: &api.Rule_HTTP_Path{
					Path: &api.Rule_StringMatch{
						Match: &api.Rule_StringMatch_Regexp{
							Regexp: match.PathRegex,
						},
					},
				},
			},
		},
	})
}

func appendHeaderFilter(matchParts []*api.Rule, match specs.HTTPMatch) []*api.Rule {
	if len(match.Headers) == 0 {
		return matchParts
	}
	var h []*api.Rule_Header
	for name, value := range match.Headers {
		h = append(h, &api.Rule_Header{
			Key: name,
			Vallue: &api.Rule_StringMatch{
				Match: &api.Rule_StringMatch_Regexp{
					Regexp: value,
				},
			},
		})
	}

	return append(matchParts, &api.Rule{
		Match: &api.Rule_Http{
			Http: &api.Rule_HTTP{
				Match: &api.Rule_HTTP_Headers_{
					Headers: &api.Rule_HTTP_Headers{
						Headers: h,
					},
				},
			},
		},
	})
}

func appendMethodFilter(matchParts []*api.Rule, match specs.HTTPMatch) []*api.Rule {
	if len(match.Methods) == 0 {
		return matchParts
	}
	var methods []api.Rule_HTTP_Method
	for _, m := range match.Methods {
		if m == "*" {
			methods = []api.Rule_HTTP_Method{api.Rule_HTTP_ALL}
			break
		}
		u := strings.ToUpper(m)
		v, ok := api.Rule_HTTP_Method_value[u]
		if ok {
			methods = append(methods, api.Rule_HTTP_Method(v))
		}
	}
	return append(matchParts, &api.Rule{
		Match: &api.Rule_Http{
			Http: &api.Rule_HTTP{
				Match: &api.Rule_HTTP_Methods_{
					Methods: &api.Rule_HTTP_Methods{
						Methods: methods,
					},
				},
			},
		},
	})
}

func buildHTTPRuleFromService(svc *topology.Service) *api.Rule {
	return &api.Rule{
		Match: &api.Rule_Any{
			Any: &api.Rule_List{
				Rules: []*api.Rule{
					{
						Match: &api.Rule_Http{
							Http: &api.Rule_HTTP{
								Match: &api.Rule_HTTP_Host{
									Host: fmt.Sprintf("%s.%s.%s", svc.Name, svc.Namespace, subdomain),
								},
							},
						},
					},
					{
						Match: &api.Rule_Http{
							Http: &api.Rule_HTTP{
								Match: &api.Rule_HTTP_Host{
									Host: svc.ClusterIP,
								},
							},
						},
					},
				},
			},
		},
	}
}

func buildHTTPRuleFromTrafficTarget(tt *topology.ServiceTrafficTarget, ttSvc *topology.Service) *api.Rule {
	ttRule := buildHTTPRuleFromTrafficSpecs(tt.Rules)
	svcRule := buildHTTPRuleFromService(ttSvc)
	if ttRule != nil {
		return &api.Rule{
			Match: &api.Rule_All{
				All: &api.Rule_List{
					Rules: []*api.Rule{ttRule, svcRule},
				},
			},
		}
	}
	return svcRule
}

func buildHTTPRuleFromTrafficSplit(ts *topology.TrafficSplit, tsSvc *topology.Service) *api.Rule {
	tsRule := buildHTTPRuleFromTrafficSpecs(ts.Rules)
	svcRule := buildHTTPRuleFromService(tsSvc)
	if tsRule != nil {
		return &api.Rule{
			Match: &api.Rule_All{
				All: &api.Rule_List{
					Rules: []*api.Rule{tsRule, svcRule},
				},
			},
		}
	}
	return svcRule
}

func buildHTTPRuleFromTrafficTargetIndirect(tt *topology.ServiceTrafficTarget, ttSvc *topology.Service) *api.Rule {
	ttRule := buildHTTPRuleFromTrafficSpecs(tt.Rules)
	svcRule := buildHTTPRuleFromService(ttSvc)
	indirectRule := &api.Rule{
		Match: &api.Rule_Http{
			Http: &api.Rule_HTTP{
				Match: &api.Rule_HTTP_Headers_{
					Headers: &api.Rule_HTTP_Headers{
						Headers: []*api.Rule_Header{
							{Key: "X-Forwarded-For", Vallue: &api.Rule_StringMatch{
								Match: &api.Rule_StringMatch_Regexp{
									Regexp: ".+",
								},
							}},
						},
					},
				},
			},
		},
	}

	if ttRule != nil {
		return &api.Rule{
			Match: &api.Rule_All{
				All: &api.Rule_List{
					Rules: []*api.Rule{svcRule, ttRule, indirectRule},
				},
			},
		}
	}
	return &api.Rule{
		Match: &api.Rule_All{
			All: &api.Rule_List{
				Rules: []*api.Rule{svcRule, indirectRule},
			},
		},
	}

}

func buildHTTPRuleFromTrafficSplitIndirect(ts *topology.TrafficSplit, tsSvc *topology.Service) *api.Rule {
	tsRule := buildHTTPRuleFromTrafficSpecs(ts.Rules)
	svcRule := buildHTTPRuleFromService(tsSvc)
	indirectRule := &api.Rule{
		Match: &api.Rule_Http{
			Http: &api.Rule_HTTP{
				Match: &api.Rule_HTTP_Headers_{
					Headers: &api.Rule_HTTP_Headers{
						Headers: []*api.Rule_Header{
							{Key: "X-Forwarded-For", Vallue: &api.Rule_StringMatch{
								Match: &api.Rule_StringMatch_Regexp{
									Regexp: ".+",
								},
							}},
						},
					},
				},
			},
		},
	}

	if tsRule != nil {
		return &api.Rule{
			Match: &api.Rule_All{
				All: &api.Rule_List{
					Rules: []*api.Rule{svcRule, tsRule, indirectRule},
				},
			},
		}
	}
	return &api.Rule{
		Match: &api.Rule_All{
			All: &api.Rule_List{
				Rules: []*api.Rule{svcRule, indirectRule},
			},
		},
	}
}

func buildTCPRouterRule(sni ...string) *api.Rule {
	s := "*"
	if len(sni) > 0 {
		s = sni[0]
	}
	return &api.Rule{Match: &api.Rule_Tcp{
		Tcp: &api.Rule_TCP{
			Match: &api.Rule_TCP_Sni{
				Sni: s,
			},
		},
	}}
}
