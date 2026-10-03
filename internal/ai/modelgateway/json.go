package modelgateway

// ValidateUnambiguousJSON shares the bounded duplicate-key check with the
// framed tool contract, so replay and dispatch use the same JSON semantics.
func ValidateUnambiguousJSON(data []byte) error { return unambiguousJSON(data) }
