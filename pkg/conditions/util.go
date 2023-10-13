package conditions

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
