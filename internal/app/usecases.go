package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"po-trans/internal/domain"
)

// Application bundles the use cases for the transport layer.
type Application struct {
	CreateTransfer *CreateTransfer
	GetTransfer    *GetTransfer
	GetBalance     *GetBalance
}

// New wires the use cases to the storage ports.
func New(writer TransferWriter, reader Reader) *Application {
	return &Application{
		CreateTransfer: NewCreateTransfer(writer, reader),
		GetTransfer:    NewGetTransfer(reader),
		GetBalance:     NewGetBalance(reader),
	}
}

// CreateTransfer is the transfer command.
type CreateTransfer struct {
	writer TransferWriter
	reader TransferReader
}

// NewCreateTransfer wires the command to its ports.
func NewCreateTransfer(writer TransferWriter, reader TransferReader) *CreateTransfer {
	return &CreateTransfer{writer: writer, reader: reader}
}

// Handle executes a transfer request.
//
// Business rejections are persisted by the writer and surface here as the
// matching domain sentinel (ErrInsufficientFunds, ErrSelfTransfer,
// ErrAccountNotFound); a conflicting repeat is domain.ErrIdempotencyConflict;
// invalid input is a *domain.ValidationError.
func (c *CreateTransfer) Handle(ctx context.Context, req TransferRequest) (domain.Transfer, error) {
	cmd, err := req.Command()
	if err != nil {
		var ve *domain.ValidationError
		if errors.As(err, &ve) && ve.Field == domain.FieldIdempotencyKey {
			return domain.Transfer{}, err // without a valid key there is nothing to look up
		}
		return domain.Transfer{}, c.invalidFields(ctx, req.IdempotencyKey.Value, err)
	}
	cmd.TransferID, err = newTransferID()
	if err != nil {
		return domain.Transfer{}, err
	}
	t, err := c.writer.Transfer(ctx, cmd)
	if err != nil {
		return domain.Transfer{}, err
	}
	if rejection := t.RejectionError(); rejection != nil {
		return domain.Transfer{}, rejection
	}
	return t, nil
}

// invalidFields resolves a request whose fields are invalid: with an existing
// key the request differs from the stored one by definition, so it is a
// conflict; without one it is a validation error.
func (c *CreateTransfer) invalidFields(ctx context.Context, key string, validationErr error) error {
	_, err := c.reader.GetByKey(ctx, key)
	switch {
	case err == nil:
		return domain.ErrIdempotencyConflict
	case errors.Is(err, domain.ErrTransferNotFound):
		return validationErr
	default:
		return fmt.Errorf("look up idempotency key: %w", err)
	}
}

// GetTransfer is the query for a transfer by idempotency key.
type GetTransfer struct{ reader TransferReader }

// NewGetTransfer wires the query to its port.
func NewGetTransfer(reader TransferReader) *GetTransfer { return &GetTransfer{reader: reader} }

// Handle returns the stored transfer or domain.ErrTransferNotFound. A key that
// cannot be stored (too long, control characters, invalid UTF-8) is not found
// without asking the storage.
func (g *GetTransfer) Handle(ctx context.Context, key string) (domain.Transfer, error) {
	if !domain.ValidIdempotencyKey(key) {
		return domain.Transfer{}, domain.ErrTransferNotFound
	}
	return g.reader.GetByKey(ctx, key)
}

// GetBalance is the query for an account balance.
type GetBalance struct{ reader AccountReader }

// NewGetBalance wires the query to its port.
func NewGetBalance(reader AccountReader) *GetBalance { return &GetBalance{reader: reader} }

// Handle returns the account or domain.ErrAccountNotFound. An id that cannot be
// stored is not found without asking the storage.
func (g *GetBalance) Handle(ctx context.Context, id string) (domain.Account, error) {
	if !domain.ValidAccountID(id) {
		return domain.Account{}, domain.ErrAccountNotFound
	}
	return g.reader.GetAccount(ctx, id)
}

// newTransferID generates a UUIDv7: not guessable, ordered in time (so it suits
// the primary key index) and independent of the database version.
func newTransferID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate transfer id: %w", err)
	}
	return id.String(), nil
}
