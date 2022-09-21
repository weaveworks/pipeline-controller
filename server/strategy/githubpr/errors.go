package githubpr

import "fmt"

type ErrMissingSecretField struct {
	fieldName string
}

func (e ErrMissingSecretField) Error() string {
	return fmt.Sprintf("missing field in Secret: %s", e.fieldName)
}
