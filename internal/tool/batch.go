package tool

import (
	"errors"
	"fmt"
)

// Batch is the result of a multi-item mutation. data carries the per-item
// report (items[].error from [ItemError]); applied and already count items
// that took effect or were no-ops; errs holds one error per failed item.
//
// When nothing took effect and at least one item failed, the result is an
// error: its data is the error body of the dominant error ([ErrOutcomeUnknown]
// wins over any other) plus "items": data, and Next is that body's Next
// followed by next. When some items failed but others took effect, the
// result is OK(data) with a warning. Otherwise it is OK(data).Then(next...).
func Batch(data any, applied, already int, errs []error, next ...NextStep) *Result {
	if len(errs) == 0 {
		return OK(data).Then(next...)
	}
	if applied+already == 0 {
		dominant := errs[0]
		for _, err := range errs {
			if errors.Is(err, ErrOutcomeUnknown) {
				dominant = err
				break
			}
		}
		b := BodyFor(dominant)
		steps := append(append([]NextStep(nil), b.Next...), next...)
		return &Result{Data: batchFailure{ErrorBody: b, Items: data}, IsError: true, Next: steps}
	}
	total := applied + already + len(errs)
	return OK(data).Warn(fmt.Sprintf("%d of %d items failed; see items[].error", len(errs), total)).Then(next...)
}

// batchFailure is the data of an all-items-failed [Batch]: the dominant
// error body with the per-item report beside it.
type batchFailure struct {
	ErrorBody
	Items any `json:"items"`
}

// ItemError classifies one failed item for a batch report. outcome is
// "unknown" when the item may have been applied ([ErrOutcomeUnknown]) and
// "error" otherwise; body is the structured error.
func ItemError(err error) (outcome string, body ErrorBody) {
	if errors.Is(err, ErrOutcomeUnknown) {
		return "unknown", BodyFor(err)
	}
	return "error", BodyFor(err)
}
