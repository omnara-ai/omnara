package httpapi

import "github.com/omnara-ai/omnara/internal/modelprovider"

func WithModelDiscoverer(discoverer modelprovider.DiscoverFunc) Option {
	return func(s *Server) {
		s.modelDiscoverer = discoverer
	}
}
