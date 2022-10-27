package server

import (
	"net/http"

	"github.com/go-logr/logr"

	"github.com/weaveworks/pipeline-controller/server/strategy"
	kuberecorder "k8s.io/client-go/tools/record"
)

func Logger(l logr.Logger) Opt {
	return func(s *PromotionServer) error {
		s.log = l
		return nil
	}
}

func ListenAddr(addr string) Opt {
	return func(s *PromotionServer) error {
		s.addr = addr
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

func StrategyRegistry(stratReg strategy.StrategyRegistry) Opt {
	return func(s *PromotionServer) error {
		s.stratReg = stratReg
		return nil
	}
}

func EventRecorder(eventRecorder kuberecorder.EventRecorder) Opt {
	return func(s *PromotionServer) error {
		s.eventRecorder = eventRecorder
		return nil
	}
}
