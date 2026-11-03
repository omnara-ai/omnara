package storeutil

func ValueOrZero[T any](value *T) (zero T) {
	if value != nil {
		return *value
	}
	return zero
}
