package server

import (
	"net/http"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Logger(l logr.Logger) Opt {
	return func(s *PromotionServer) error {
		s.log = l
		return nil
	}
}

func Client(c client.Client) Opt {
	return func(s *PromotionServer) error {
		s.c = c
		return nil
	}
}

func ListenAddr(addr string) Opt {
	return func(s *PromotionServer) error {
		s.addr = addr
		return nil
	}
}

func PromotionStrategy(strategy Strategy) Opt {
	return func(s *PromotionServer) error {
		s.promStrategy = strategy
		return nil
	}
}

func PromotionHandler(hndlr http.HandlerFunc) Opt {
	return func(s *PromotionServer) error {
		s.promHandler = hndlr
		return nil
	}
}

func PromotionEndpointName(n string) Opt {
	return func(s *PromotionServer) error {
		s.promEndpointName = n
		return nil
	}
}
