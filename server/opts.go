package server

import (
	"net/http"

	"github.com/go-logr/logr"

	"github.com/weaveworks/pipeline-controller/server/strategy"
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

func WithRetry(delay, maxDelay, threshold int) Opt {
	return func(s *PromotionServer) error {
		s.retry.Delay = delay
		s.retry.MaxDelay = maxDelay
		s.retry.Threshold = threshold

		return nil
	}
}
