package conditions

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	ReadyCondition            = "Ready"
	PromotionPendingCondition = "PromotionPending"
)

func IsReady(cs []metav1.Condition) bool {
	for _, c := range cs {
		if c.Type == "Ready" {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

func IsReadyUnstructured(cs []interface{}) bool {
	for _, obj := range cs {
		if cond, ok := obj.(map[string]interface{}); ok {
			s, ok, err := unstructured.NestedString(cond, "type")
			if ok && err == nil {
				if s == "Ready" {
					s, ok, err = unstructured.NestedString(cond, "status")
					if ok && err == nil {
						return s == string(metav1.ConditionTrue)
					}
					return false
				}
			}
		}
	}
	return false
}
