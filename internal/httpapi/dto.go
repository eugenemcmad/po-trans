package httpapi

import (
	"time"

	"po-trans/internal/domain"
)

// createTransferResponse is the 200 body of POST /transfers. It has no reason
// and no timestamp: the contract does not have them.
type createTransferResponse struct {
	TransferID    string `json:"transfer_id"`
	Status        string `json:"status"`
	FromAccountID string `json:"from_account_id"`
	ToAccountID   string `json:"to_account_id"`
	Amount        int64  `json:"amount"`
}

// getTransferResponse is the 200 body of GET /transfers/{key}. Reason appears
// only for rejected transfers.
type getTransferResponse struct {
	TransferID     string `json:"transfer_id"`
	IdempotencyKey string `json:"idempotency_key"`
	FromAccountID  string `json:"from_account_id"`
	ToAccountID    string `json:"to_account_id"`
	Amount         int64  `json:"amount"`
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type accountResponse struct {
	AccountID string `json:"account_id"`
	Balance   int64  `json:"balance"`
}

type statusResponse struct {
	Status string `json:"status"`
}

func newCreateTransferResponse(t domain.Transfer) createTransferResponse {
	return createTransferResponse{
		TransferID:    t.ID,
		Status:        string(t.Status),
		FromAccountID: t.FromAccountID,
		ToAccountID:   t.ToAccountID,
		Amount:        t.Amount,
	}
}

func newGetTransferResponse(t domain.Transfer) getTransferResponse {
	return getTransferResponse{
		TransferID:     t.ID,
		IdempotencyKey: t.IdempotencyKey,
		FromAccountID:  t.FromAccountID,
		ToAccountID:    t.ToAccountID,
		Amount:         t.Amount,
		Status:         string(t.Status),
		Reason:         string(t.Reason),
		CreatedAt:      formatTime(t.CreatedAt),
	}
}

// formatTime renders RFC 3339 in UTC with second precision (fraction truncated).
func formatTime(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}
