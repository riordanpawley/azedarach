package issues

import (
	"context"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// InteractionNoticeAdapter is an optional presentation seam. Implementations
// may project notices, but notice lifecycle must never mutate request truth.
type InteractionNoticeAdapter interface {
	ProjectInteractionNotice(context.Context, domain.InteractionRequest) error
}
