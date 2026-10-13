// Package memstore is an in-memory implementation of every application port,
// shared by the unit tests. It mirrors the observable behaviour of the
// PostgreSQL repositories but knows nothing about row locks or transactions,
// so concurrency is tested on PostgreSQL only.
package memstore

import (
	"context"
	"fmt"
	"sync"
	"time"

	"po-trans/internal/domain"
)

// Store implements the app ports (TransferWriter, TransferReader, AccountReader) and the readiness probe.
type Store struct {
	mu        sync.Mutex
	accounts  map[string]int64
	transfers map[string]domain.Transfer

	// The fields below are configuration, read without the mutex: set them
	// before the store is used and do not change them while requests run.

	// BeforeTransfer, if set, runs at the start of every Transfer call (outside
	// the lock). Tests use it to make a request slow or to observe its start.
	BeforeTransfer func(ctx context.Context)
	// PingErr, if set, is returned by Ping.
	PingErr error
	// Err, if set, is returned by every operation (simulates a storage failure).
	Err error
}

// newStore creates a store with the given account balances.
func newStore(balances map[string]int64) *Store {
	s := &Store{accounts: make(map[string]int64, len(balances)), transfers: map[string]domain.Transfer{}}
	for id, b := range balances {
		s.accounts[id] = b
	}
	return s
}

// Seeded creates a store with acc_1 … acc_10, 1000 coins each.
func Seeded() *Store {
	b := make(map[string]int64, 10)
	for i := 1; i <= 10; i++ {
		b[fmt.Sprintf("acc_%d", i)] = 1000
	}
	return newStore(b)
}

// Balance returns the balance of id (0 if absent), for assertions.
func (s *Store) Balance(id string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accounts[id]
}

// TransferCount returns the number of stored transfers, for assertions.
func (s *Store) TransferCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.transfers)
}

// Ping implements httpapi.Pinger.
func (s *Store) Ping(context.Context) error { return s.PingErr }

// GetAccount implements app.AccountReader.
func (s *Store) GetAccount(_ context.Context, id string) (domain.Account, error) {
	if s.Err != nil {
		return domain.Account{}, s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.accounts[id]
	if !ok {
		return domain.Account{}, domain.ErrAccountNotFound
	}
	return domain.Account{ID: id, Balance: b}, nil
}

// GetByKey implements app.TransferReader.
func (s *Store) GetByKey(_ context.Context, key string) (domain.Transfer, error) {
	if s.Err != nil {
		return domain.Transfer{}, s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.transfers[key]
	if !ok {
		return domain.Transfer{}, domain.ErrTransferNotFound
	}
	return t, nil
}

// Transfer implements app.TransferWriter. The rules come from domain.Decide,
// as in the PostgreSQL repository; only moving the money differs (maps here,
// locked rows and UPDATEs there), so the two are not merged.
func (s *Store) Transfer(ctx context.Context, cmd domain.TransferCommand) (domain.Transfer, error) {
	if s.BeforeTransfer != nil {
		s.BeforeTransfer(ctx)
	}
	if s.Err != nil {
		return domain.Transfer{}, s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.transfers[cmd.IdempotencyKey]; ok {
		if !cmd.SameAs(existing) {
			return domain.Transfer{}, domain.ErrIdempotencyConflict
		}
		return existing, nil
	}
	t := cmd.NewCompleted()
	t.CreatedAt = time.Now().UTC()

	reason, err := domain.Decide(cmd, s.load)
	if err != nil {
		return domain.Transfer{}, err
	}
	if reason != "" {
		t = t.Reject(reason)
	} else {
		s.accounts[cmd.FromAccountID] -= cmd.Amount
		s.accounts[cmd.ToAccountID] += cmd.Amount
	}
	s.transfers[cmd.IdempotencyKey] = t
	return t, nil
}

// load serves the domain rules from the maps; the caller holds the lock.
func (s *Store) load(id string) (*domain.Account, error) {
	b, ok := s.accounts[id]
	if !ok {
		return nil, nil
	}
	return &domain.Account{ID: id, Balance: b}, nil
}
