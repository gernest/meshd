package safe

import (
	"fmt"

	"github.com/cenkalti/backoff/v4"
	"github.com/gernest/meshd/pkg/zlg"
)

// OperationWithRecover wrap a backoff operation in a Recover.
func OperationWithRecover(operation backoff.Operation) backoff.Operation {
	return func() (err error) {
		defer func() {
			if res := recover(); res != nil {

				err = fmt.Errorf("panic in operation: %w", err)
				zlg.Info(err.Error())
			}
		}()

		return operation()
	}
}
