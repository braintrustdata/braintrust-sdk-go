package eval

import (
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// NewEvaluator creates an evaluator using the legacy API client's transport.
//
// Deprecated: Use [NewEvaluatorWithClient] with a generated [openapi.Client],
// or braintrust.NewEvaluator for SDK-managed configuration.
func NewEvaluator[I, R any](s *auth.Session, tp *trace.TracerProvider, client *api.API, project string) *Evaluator[I, R] {
	var generated *openapi.Client
	if client != nil {
		generated = client.GeneratedClient()
	}
	return NewEvaluatorWithClient[I, R](s, tp, generated, project)
}
