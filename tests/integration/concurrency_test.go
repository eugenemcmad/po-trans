package integration

import (
	"fmt"
	"math/rand"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"po-trans/internal/repository/postgres"
)

// randomSeed is fixed so a failing run can be repeated; it is logged on failure.
const randomSeed = 20261012

// C1: many random transfers at once never break the invariants.
func TestC1_RandomTransfers(t *testing.T) {
	const total, inFlight = 200, 50
	pool := freshSeeded(t)
	a := api{t, newServer(t, pool, postgres.DefaultRetryPolicy())}

	rnd := rand.New(rand.NewSource(randomSeed)) //nolint:gosec // fixed seed: the run must be reproducible
	bodies := make([]string, total)
	for i := range bodies {
		from := rnd.Intn(10) + 1
		to := rnd.Intn(10) + 1
		bodies[i] = body(fmt.Sprintf("c1-%d", i), fmt.Sprintf("acc_%d", from), fmt.Sprintf("acc_%d", to), int64(rnd.Intn(600)+1))
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("random seed: %d", randomSeed)
		}
	})

	replies, err := a.postAll(total, inFlight, func(i int) string { return bodies[i] })
	require.NoError(t, err)

	for i, r := range replies {
		require.Contains(t, []int{http.StatusOK, http.StatusUnprocessableEntity}, r.Code, "request %d: %s", i, bodies[i])
	}
	require.Equal(t, total, count(t, pool, `SELECT count(*) FROM transfers`))
	requireInvariants(t, pool)
}

// C2: opposite transfers between the same two accounts never deadlock. With
// MaxRetries 0 a deadlock would not be hidden by a retry and would surface as 500.
func TestC2_OppositeTransfersDoNotDeadlock(t *testing.T) {
	const total = 200
	pool := freshSeeded(t)
	a := api{t, newServer(t, pool, postgres.RetryPolicy{MaxRetries: 0})}

	replies, err := a.postAll(total, 20, func(i int) string {
		from, to := "acc_1", "acc_2"
		if i%2 == 1 {
			from, to = to, from
		}
		return body(fmt.Sprintf("c2-%d", i), from, to, 1)
	})
	require.NoError(t, err)

	for i, r := range replies {
		require.Equal(t, http.StatusOK, r.Code, "request %d: %s", i, r.Body)
	}
	require.EqualValues(t, 1000, balanceOf(t, pool, "acc_1"), "equal numbers each way")
	requireInvariants(t, pool)
}

// C3: the same request sent many times at once is applied exactly once.
func TestC3_SameKeySameBody(t *testing.T) {
	const copies = 20
	pool := freshSeeded(t)
	a := api{t, newServer(t, pool, postgres.DefaultRetryPolicy())}

	replies, err := a.postAll(copies, copies, func(int) string { return body("c3", "acc_1", "acc_2", 100) })
	require.NoError(t, err)

	for i, r := range replies {
		require.Equal(t, http.StatusOK, r.Code, "copy %d: %s", i, r.Body)
		require.Equal(t, replies[0], r, "copy %d answers like copy 0, same transfer_id", i)
	}
	require.EqualValues(t, 900, balanceOf(t, pool, "acc_1"))
	require.Equal(t, 1, count(t, pool, `SELECT count(*) FROM transfers`))
	requireInvariants(t, pool)
}

// C4: one key, two different bodies, many copies of each, all at once.
func TestC4_SameKeyDifferentBodies(t *testing.T) {
	const copies = 20
	pool := freshSeeded(t)
	a := api{t, newServer(t, pool, postgres.DefaultRetryPolicy())}

	bodyA := body("c4", "acc_1", "acc_2", 10)
	bodyB := body("c4", "acc_1", "acc_3", 20)
	replies, err := a.postAll(2*copies, 2*copies, func(i int) string {
		if i%2 == 0 {
			return bodyA
		}
		return bodyB
	})
	require.NoError(t, err)

	// The stored transfer says who won.
	var winnerAmount int64
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT amount FROM transfers WHERE idempotency_key = 'c4'`).Scan(&winnerAmount))
	winnerIsA := winnerAmount == 10

	var winnerReply *reply
	for i, r := range replies {
		isA := i%2 == 0
		require.NotEqual(t, http.StatusInternalServerError, r.Code, "copy %d: %s", i, r.Body)
		if isA == winnerIsA {
			require.Equal(t, http.StatusOK, r.Code, "copy %d: %s", i, r.Body)
			if winnerReply == nil {
				winnerReply = &replies[i]
			}
			require.Equal(t, *winnerReply, r, "winners get the same answer")
		} else {
			require.Equal(t, http.StatusConflict, r.Code, "copy %d: %s", i, r.Body)
		}
	}
	require.Equal(t, 1, count(t, pool, `SELECT count(*) FROM transfers`))
	require.Equal(t, 1000-winnerAmount, balanceOf(t, pool, "acc_1"), "money moved once")
	requireInvariants(t, pool)
}

// C5: parallel debits of one account never overdraw it.
func TestC5_ParallelDebitsOfOneAccount(t *testing.T) {
	const requests = 20
	pool := freshSeeded(t)
	a := api{t, newServer(t, pool, postgres.DefaultRetryPolicy())}

	replies, err := a.postAll(requests, requests, func(i int) string {
		return body(fmt.Sprintf("c5-%d", i), "acc_1", "acc_2", 100)
	})
	require.NoError(t, err)

	counts := map[int]int{}
	for _, r := range replies {
		counts[r.Code]++
	}
	require.Equal(t, map[int]int{http.StatusOK: 10, http.StatusUnprocessableEntity: 10}, counts)
	require.EqualValues(t, 0, balanceOf(t, pool, "acc_1"))
	require.EqualValues(t, 2000, balanceOf(t, pool, "acc_2"))
	requireInvariants(t, pool)
}
