package mesh

import "context"

type correlationKey struct{}
type correlation struct{ trace, request string }

// TraceID identifies a chain of authenticated mesh calls.
func TraceID(ctx context.Context) string {
	value, _ := ctx.Value(correlationKey{}).(correlation)
	return value.trace
}

// RequestID identifies the current authenticated HTTP request.
func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(correlationKey{}).(correlation)
	return value.request
}

func validCorrelation(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' || character == ':') {
			return false
		}
	}
	return true
}
