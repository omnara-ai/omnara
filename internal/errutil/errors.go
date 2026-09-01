package errutil

import "errors"

func OnlyMatches(err, target error) bool {
	if err == nil || target == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !OnlyMatches(cause, target) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return OnlyMatches(wrapped.Unwrap(), target)
	}
	return errors.Is(err, target)
}
